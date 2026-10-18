package qwen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"tokenhub/internal/provider"
)

// 千问网页版没有 OpenAI tools 参数，这里把工具协议桥接到文本层：
// 请求侧把工具清单折叠进 system prompt，要求模型用 <tool_call>{"name":..,"arguments":{..}}</tool_call> 发起调用；
// 响应侧流式扫描 <tool_call> 块，翻译回 OpenAI tool_calls 增量。口径移植自 Qwen2OpenAI tools.py（实测协议）。

const (
	tcOpen  = "<tool_call>"
	tcClose = "</tool_call>"
)

// ── 请求侧：工具清单 → system prompt ──────────────────────────────

// compressSchemaType 把 JSON Schema 压成一行类型签名（如 "{ a: string; b?: number[] }"）。
func compressSchemaType(schema map[string]any) string {
	if schema == nil {
		return "any"
	}
	if ev, ok := schema["enum"].([]any); ok && len(ev) > 0 {
		parts := make([]string, 0, len(ev))
		for _, v := range ev {
			b, _ := json.Marshal(v)
			parts = append(parts, string(b))
		}
		return strings.Join(parts, " | ")
	}
	t, _ := schema["type"].(string)
	if t == "" {
		if arr, ok := schema["type"].([]any); ok {
			parts := make([]string, 0, len(arr))
			for _, tt := range arr {
				if s, ok := tt.(string); ok {
					next := map[string]any{}
					for k, v := range schema {
						next[k] = v
					}
					next["type"] = s
					parts = append(parts, compressSchemaType(next))
				}
			}
			return strings.Join(parts, " | ")
		}
		t = "any"
	}
	switch t {
	case "array":
		items, _ := schema["items"].(map[string]any)
		return compressSchemaType(items) + "[]"
	case "object":
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			return "object"
		}
		required := map[string]bool{}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
		var fields []string
		for key, val := range props {
			opt := "?"
			if required[key] {
				opt = ""
			}
			m, _ := val.(map[string]any)
			fields = append(fields, fmt.Sprintf("%s%s: %s", key, opt, compressSchemaType(m)))
		}
		return "{ " + strings.Join(fields, "; ") + " }"
	}
	return t
}

// compressToolDefinition 单个工具压成两行签名。
func compressToolDefinition(tool map[string]any) string {
	fn, _ := tool["function"].(map[string]any)
	if fn == nil {
		fn = tool
	}
	name, _ := fn["name"].(string)
	if name == "" {
		name = "unknown"
	}
	desc, _ := fn["description"].(string)
	desc = strings.TrimSpace(desc)
	params, _ := fn["parameters"].(map[string]any)
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	sig := compressSchemaType(params)
	if desc != "" {
		return fmt.Sprintf("- %s%s\n  %s", name, sig, desc)
	}
	return fmt.Sprintf("- %s%s", name, sig)
}

// buildToolSystemPrompt 生成注入 system 的工具协议说明。
func buildToolSystemPrompt(tools []json.RawMessage, mustCall bool) string {
	if len(tools) == 0 {
		return ""
	}
	var defs []string
	for _, raw := range tools {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		defs = append(defs, compressToolDefinition(m))
	}
	if len(defs) == 0 {
		return ""
	}
	lines := []string{
		"CRITICAL: You MUST use the tools below to accomplish tasks. Never describe what you want to do — emit the exact tool call XML.",
		"",
		"## Available Tools",
		strings.Join(defs, "\n"),
		"",
		"## How to call a tool",
		"Output EXACTLY this format, nothing else:",
		tcOpen,
		`{"name": "tool_name", "arguments": {"arg1": "value1"}}`,
		tcClose,
		"",
		"## Receiving results",
		"Tool results come back as:",
		`<tool_response tool_call_id="..." name="tool_name">`,
		"...result...",
		"</tool_response>",
		"",
		"## Rules",
		"- ONE `<tool_call>` block per tool invocation.",
		"- JSON inside MUST be valid, on one logical line.",
		"- Use the exact tool name from the list above. Calling ANY other name (e.g. Read, Write, Edit, Grep, LS, Task, TodoWrite, run_mcp) is FORBIDDEN and will be rejected — even if such tools existed in other environments.",
		"- Provide all required arguments; skip unknown ones.",
		"- Do NOT wrap in code fences or markdown.",
		"- Do NOT explain what you are about to do. Just emit the XML.",
		"- Do NOT write phrases like \"Tool Xxx does not exists\" — that is an environment error, not your output. If you cannot use a tool, answer in plain text instead.",
	}
	if mustCall {
		lines = append(lines, "- You MUST call at least one tool before writing anything else.")
	}
	return strings.Join(lines, "\n")
}

// parseToolCallPayload 解析 <tool_call> 内的 JSON（容忍代码围栏/别名字段）。
func parseToolCallPayload(raw string) (name string, args json.RawMessage) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		// 容忍 ```json 围栏
		if i := strings.Index(text, "```"); i >= 0 {
			rest := text[i+3:]
			rest = strings.TrimPrefix(rest, "json")
			if j := strings.Index(rest, "```"); j >= 0 {
				text = strings.TrimSpace(rest[:j])
			}
		}
		m = nil
		if json.Unmarshal([]byte(text), &m) != nil {
			return "", nil
		}
	}
	for _, k := range []string{"name", "tool", "function"} {
		if s, ok := m[k].(string); ok && s != "" {
			name = s
			break
		}
	}
	if name == "" {
		return "", nil
	}
	var argsAny any
	for _, k := range []string{"arguments", "parameters", "args"} {
		if v, ok := m[k]; ok && v != nil {
			argsAny = v
			break
		}
	}
	if argsAny == nil {
		argsAny = map[string]any{}
	}
	b, err := json.Marshal(argsAny)
	if err != nil {
		return name, json.RawMessage(`{}`)
	}
	return name, json.RawMessage(b)
}

// ── 响应侧：流式扫描 <tool_call> 块 ───────────────────────────────

type completedCall struct {
	Index int
	ID    string
	Name  string
	Args  string
}

type toolCallStreamParser struct {
	pendingText  string
	inCall       bool
	callBuffer   string
	emittedCount int
	known        map[string]bool // 请求声明的工具名白名单；nil=本会话未启用工具
}

// newToolCallParser 构造桥接扫描器。known 为请求 tools 里的函数名集合：
// 非 nil 时只转发白名单内的调用（千问偶尔幻觉出 Task/LS/run_mcp 等列表外
// 工具名，转发必被客户端报 "Tool Xxx does not exists" 并打断会话）；
// nil 表示请求未带 tools，任何调用都不转发。
func newToolCallParser(known map[string]bool) *toolCallStreamParser {
	return &toolCallStreamParser{known: known}
}

// rejectNote 生成被拒调用的提示文本（随正文下发，模型下一轮可见，可自我纠正）。
func (p *toolCallStreamParser) rejectNote(name string) string {
	if p.known == nil {
		return fmt.Sprintf("\n[ToolHub] Tool call to %q was ignored: no tools are enabled in this session. Reply in plain text.\n", name)
	}
	names := make([]string, 0, len(p.known))
	for n := range p.known {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Sprintf("\n[ToolHub] Tool call to %q was rejected: not in this session's tool list. Available tools: %s. Use ONLY these names.\n", name, strings.Join(names, ", "))
}

func (p *toolCallStreamParser) allowed(name string) bool {
	return p.known != nil && p.known[name]
}

// push 输入一段增量文本，返回 (可安全下发的文本, 本次完成的调用)。
func (p *toolCallStreamParser) push(chunk string) (string, []completedCall) {
	var textOut strings.Builder
	var calls []completedCall
	buffer := chunk
	for buffer != "" {
		if p.inCall {
			p.callBuffer += buffer
			buffer = ""
			idx := strings.Index(p.callBuffer, tcClose)
			if idx == -1 {
				break
			}
			inner := p.callBuffer[:idx]
			buffer = p.callBuffer[idx+len(tcClose):]
			p.callBuffer = ""
			if name, args := parseToolCallPayload(inner); name != "" {
				if !p.allowed(name) {
					textOut.WriteString(p.rejectNote(name))
				} else {
					calls = append(calls, completedCall{
						Index: p.emittedCount,
						ID:    "call_" + provider.NewUUID(),
						Name:  name,
						Args:  string(args),
					})
					p.emittedCount++
				}
			}
			p.inCall = false
			continue
		}
		p.pendingText += buffer
		buffer = ""
		idx := strings.Index(p.pendingText, tcOpen)
		if idx != -1 {
			if before := p.pendingText[:idx]; before != "" {
				textOut.WriteString(before)
			}
			buffer = p.pendingText[idx+len(tcOpen):]
			p.pendingText = ""
			p.inCall = true
			continue
		}
		// 没有 open tag：可能尾部是半个 tag，扣住等待下一段
		safe, rest := splitSafeText(p.pendingText)
		if safe != "" {
			textOut.WriteString(safe)
		}
		p.pendingText = rest
	}
	return textOut.String(), calls
}

// flush 流结束时调用：吐出残留文本/未闭合的调用。
func (p *toolCallStreamParser) flush() (string, []completedCall) {
	var calls []completedCall
	textOut := p.pendingText
	p.pendingText = ""
	if p.inCall && p.callBuffer != "" {
		if name, args := parseToolCallPayload(p.callBuffer); name != "" {
			if !p.allowed(name) {
				textOut += p.rejectNote(name)
			} else {
				calls = append(calls, completedCall{
					Index: p.emittedCount,
					ID:    "call_" + provider.NewUUID(),
					Name:  name,
					Args:  string(args),
				})
				p.emittedCount++
			}
		}
		p.callBuffer = ""
		p.inCall = false
	}
	return textOut, calls
}

// splitSafeText 扣住文本尾部可能是 "<tool_call>" 前缀的部分，防止 tag 被切成两半误下发。
func splitSafeText(text string) (safe, rest string) {
	// 找最长后缀使其成为 tcOpen 的真前缀（如 "<tool_" → 扣住等待下一段补全）
	max := len(tcOpen) - 1
	if len(text) < max {
		max = len(text)
	}
	for i := max; i > 0; i-- {
		suffix := text[len(text)-i:]
		if strings.HasPrefix(tcOpen, suffix) {
			return text[:len(text)-i], suffix
		}
	}
	return text, ""
}
