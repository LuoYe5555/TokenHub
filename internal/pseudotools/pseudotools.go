// Package pseudotools 给没有原生工具能力的模型补工具调用：
// 请求侧剥离 tools 定义、注入协议提示词、把历史里的工具往返转成文本；
// 响应侧解析模型按协议输出的 <tool_call> 标记，还原成标准 OpenAI tool_calls。
// 对客户端完全透明——它始终看到原生 tool_calls 形状。
package pseudotools

import (
	"encoding/json"
	"regexp"
	"strings"

	"tokenhub/internal/provider"
)

const (
	openTag  = "<tool_call>"
	closeTag = "</tool_call>"
)

// invokeNameRe 从 <invoke name="xxx"> 里取工具名（兼容单引号）。
var invokeNameRe = regexp.MustCompile(`name\s*=\s*(?:"([^"]+)"|'([^']+)')`)

// invokeParamRe 匹配 <parameter name="key">。
var invokeParamRe = regexp.MustCompile(`<parameter\s+name\s*=\s*"([^"]+)"\s*>`)

// wrapperTokens 部分模型（如 ERNIE）会自带包裹标记，先剥掉再解析。
var wrapperTokens = []string{
	"<ernie:tool_call>", "</ernie:tool_call>", "ernie:tool_call",
	"<function_calls>", "</function_calls>",
}

// protocolPrompt 注入的系统提示词：约定工具清单与调用标记格式。
func protocolPrompt(tools []any) string {
	var b strings.Builder
	b.WriteString("# 工具调用协议\n")
	b.WriteString("你可以调用下列工具。需要调用时，在回复中输出（可连续输出多个）：\n")
	b.WriteString("<tool_call>{\"name\":\"工具名\",\"arguments\":{\"参数\":\"值\"}}</tool_call>\n")
	b.WriteString("输出该标记后立即停止，等待系统以 <tool_result name=\"工具名\">…</tool_result> 返回结果后再继续回答。\n")
	b.WriteString("不需要工具时直接正常回答，绝不输出 <tool_call> 标记。arguments 必须是合法 JSON 对象。\n")
	b.WriteString("只允许上述 JSON 标记格式，严禁输出 <invoke>、<parameter> 等 XML 风格的工具调用。\n")
	b.WriteString("## 可用工具\n")
	for _, tv := range tools {
		t, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := t["function"].(map[string]any)
		if fn == nil { // 兼容旧式 {"type":"function","function":...} 之外形状
			fn = t
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		b.WriteString("### " + name + "\n")
		if desc != "" {
			b.WriteString(desc + "\n")
		}
		if params, ok := fn["parameters"]; ok && params != nil {
			if raw, err := json.Marshal(params); err == nil {
				b.WriteString("参数 Schema: " + string(raw) + "\n")
			}
		}
	}
	return b.String()
}

// flattenContent 把 OpenAI 消息 content（字符串或分段数组）压成纯文本。
func flattenContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, seg := range c {
			m, ok := seg.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["text"].(string); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// Engage 改写请求：剥离原生工具字段、注入协议提示词、工具历史转文本往返。
func Engage(body []byte) []byte {
	var src map[string]any
	if json.Unmarshal(body, &src) != nil || src == nil {
		return body
	}
	tools, _ := src["tools"].([]any)
	if len(tools) == 0 {
		return body
	}
	// 1) 剥离原生工具相关字段（上游不认会直接报错）
	for _, k := range []string{"tools", "tool_choice", "functions", "function_call", "parallel_tool_calls"} {
		delete(src, k)
	}
	// 2) 历史：assistant.tool_calls → 文本标记；role=tool → user + tool_result
	msgs, _ := src["messages"].([]any)
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "assistant":
			tcs, ok := m["tool_calls"].([]any)
			if !ok || len(tcs) == 0 {
				continue
			}
			var marks strings.Builder
			for _, tc := range tcs {
				c, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := c["function"].(map[string]any)
				if fn == nil {
					continue
				}
				name, _ := fn["name"].(string)
				args := fn["arguments"]
				if s, isStr := args.(string); isStr && s != "" {
					var obj any
					if json.Unmarshal([]byte(s), &obj) == nil && obj != nil {
						args = obj
					} else {
						args = map[string]any{}
					}
				} else if args == nil {
					args = map[string]any{}
				}
				payload, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
				marks.WriteString(openTag + string(payload) + closeTag + "\n")
			}
			if marks.Len() == 0 {
				delete(m, "tool_calls")
				continue
			}
			m["content"] = flattenContent(m["content"]) + "\n" + strings.TrimRight(marks.String(), "\n")
			delete(m, "tool_calls")
		case "tool":
			result := flattenContent(m["content"])
			name, _ := m["name"].(string)
			if name == "" {
				name, _ = m["tool_call_id"].(string)
			}
			wrapped, _ := json.Marshal([]any{map[string]any{
				"type": "text",
				"text": "<tool_result name=\"" + name + "\">" + result + "</tool_result>",
			}})
			var seg any
			_ = json.Unmarshal(wrapped, &seg) // 不能赋 []byte：会被序列化成 base64 字符串
			m["role"] = "user"
			m["content"] = seg
			delete(m, "name")
			delete(m, "tool_call_id")
		}
	}
	// 3) 注入协议提示词（插到最前面的 system 位置）
	src["messages"] = append([]any{map[string]any{"role": "system", "content": protocolPrompt(tools)}}, msgs...)
	out, err := json.Marshal(src)
	if err != nil {
		return body
	}
	return out
}

// Call 解析出的单次工具调用。
type Call struct {
	Name string
	Args string // arguments 的 JSON 字符串（OpenAI 形状）
}

// extractInvokeBlocks 兼容模型自作主张输出的 XML 风格调用：
// <invoke name="tool"><parameter name="k">v</parameter>…</invoke>。
// 返回剥掉这些块后的正文与解析出的调用。
func extractInvokeBlocks(text string) (string, []Call) {
	if !strings.Contains(text, "<invoke") {
		return text, nil
	}
	var b strings.Builder
	var calls []Call
	for {
		i := strings.Index(text, "<invoke")
		if i < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:i])
		rest := text[i+len("<invoke"):]
		end := strings.Index(rest, "</invoke>")
		if end < 0 { // 未闭合：整体当正文保留
			b.WriteString("<invoke")
			b.WriteString(rest)
			text = ""
			break
		}
		block, tail := rest[:end], rest[end+len("</invoke>"):]
		text = tail
		m := invokeNameRe.FindStringSubmatch(block)
		name := ""
		if m != nil {
			if m[1] != "" {
				name = m[1]
			} else {
				name = m[2]
			}
		}
		if name == "" {
			continue
		}
		// 截掉开标签剩余部分（如 ` name="f">`），只留参数体
		if k := strings.Index(block, ">"); k >= 0 {
			block = block[k+1:]
		}
		calls = append(calls, Call{Name: name, Args: invokeArgs(block)})
	}
	return b.String(), calls
}

// invokeArgs 从 <invoke> 块内提取参数：优先 <parameter name="k">v</parameter> 列表，
// 没有时尝试整块按 JSON 解析。
func invokeArgs(block string) string {
	locs := invokeParamRe.FindAllStringSubmatchIndex(block, -1)
	if len(locs) == 0 {
		var obj map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(block)), &obj) == nil && obj != nil {
			raw, err := json.Marshal(obj)
			if err == nil {
				return string(raw)
			}
		}
		return "{}"
	}
	params := map[string]any{}
	for idx, loc := range locs {
		key := block[loc[2]:loc[3]]
		start := loc[1]
		end := len(block)
		if idx+1 < len(locs) {
			end = locs[idx+1][0]
		}
		seg := block[start:end]
		if j := strings.Index(seg, "</parameter>"); j >= 0 {
			seg = seg[:j]
		}
		val := strings.TrimSpace(seg)
		var decoded any
		if json.Unmarshal([]byte(val), &decoded) == nil { // 数字/布尔/嵌套 JSON 保留原类型
			params[key] = decoded
		} else {
			params[key] = val
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// reOpen/reClose 宽容匹配标记：部分模型会输出 <tool_call_xxxx> 之类带后缀的 tag。
var (
	reOpen  = regexp.MustCompile(`<tool_call[^>]*>`)
	reClose = regexp.MustCompile(`</tool_call[^>]*>`)
	reName  = regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
)

// repairJSON 把字符串内部的裸控制字符（真实换行/制表符）转成合法转义。
// 模型在 arguments 里嵌多行脚本时经常输出原始换行，标准 JSON 解析会直接失败。
func repairJSON(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	var b strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			if c == '"' {
				inStr = true
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			inStr = false
			b.WriteByte(c)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// salvageArgs JSON 整体解析失败时的兜底：从 "arguments" 开始做括号配对截取对象。
func salvageArgs(payload string) string {
	k := strings.Index(payload, `"arguments"`)
	if k < 0 {
		return "{}"
	}
	rest := payload[k+len(`"arguments"`):]
	i := strings.Index(rest, "{")
	if i < 0 {
		return "{}"
	}
	depth, inStr, esc := 0, false, false
	for j := i; j < len(rest); j++ {
		c := rest[j]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return repairJSON(rest[i : j+1])
			}
		}
	}
	return "{}"
}

// parseCallPayload 解析单个标记内的 JSON 载荷，返回工具名与 arguments 字符串。
func parseCallPayload(payload string) (name, args string, ok bool) {
	var m map[string]any
	if json.Unmarshal([]byte(payload), &m) != nil {
		if json.Unmarshal([]byte(repairJSON(payload)), &m) != nil {
			// 仍失败：正则兜底抠出 name，arguments 用括号配对
			if nm := reName.FindStringSubmatch(payload); nm != nil {
				return nm[1], salvageArgs(payload), true
			}
			return "", "", false
		}
	}
	name, _ = m["name"].(string)
	if name == "" {
		if nm := reName.FindStringSubmatch(payload); nm != nil {
			name = nm[1]
		}
	}
	if name == "" {
		return "", "", false
	}
	switch a := m["arguments"].(type) {
	case string:
		if strings.TrimSpace(a) == "" {
			args = "{}"
		} else {
			args = a // 已是 JSON 字符串
		}
	case nil:
		args = "{}"
	default:
		raw, err := json.Marshal(a)
		if err != nil {
			args = "{}"
		} else {
			args = string(raw)
		}
	}
	return name, args, true
}

// ParseText 从模型输出里抽取全部工具调用标记——兼容三种形态：
// ① 约定的 <tool_call>{JSON}</tool_call>（tag 容忍 _xxx 后缀）；
// ② 模型自带的 <invoke>/<parameter> XML 风格；
// ③ JSON 载荷含裸换行等常见畸形，自动修复后解析。
func ParseText(text string) (content string, calls []Call) {
	for _, w := range wrapperTokens {
		text = strings.ReplaceAll(text, w, "")
	}
	text, invCalls := extractInvokeBlocks(text)
	calls = invCalls
	var b strings.Builder
	for {
		loc := reOpen.FindStringIndex(text)
		if loc == nil {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:loc[0]])
		openTagText := text[loc[0]:loc[1]]
		rest := text[loc[1]:]
		cl := reClose.FindStringIndex(rest)
		if cl == nil {
			// 未闭合标记：整体当正文保留，避免吞内容
			b.WriteString(openTagText)
			b.WriteString(rest)
			text = ""
			break
		}
		payload := strings.TrimSpace(rest[:cl[0]])
		text = rest[cl[1]:]
		if name, args, ok := parseCallPayload(payload); ok {
			calls = append(calls, Call{Name: name, Args: args})
		}
	}
	return strings.TrimSpace(b.String()), calls
}

// ApplyToResponse 聚合响应改写：把 content 里的标记还原成标准 tool_calls。
// 没有标记时原样返回（零拷贝）。
func ApplyToResponse(raw []byte) []byte {
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return raw
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return raw
	}
	ch, ok := choices[0].(map[string]any)
	if !ok {
		return raw
	}
	msg, ok := ch["message"].(map[string]any)
	if !ok {
		return raw
	}
	text, _ := msg["content"].(string)
	content, calls := ParseText(text)
	if len(calls) == 0 {
		return raw
	}
	tcs := make([]any, 0, len(calls))
	for i, c := range calls {
		tcs = append(tcs, map[string]any{
			"id":   "call_" + provider.RandomHex(12),
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": c.Args,
			},
			"index": i,
		})
	}
	msg["content"] = content
	msg["tool_calls"] = tcs
	ch["finish_reason"] = "tool_calls"
	out, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return out
}

// ApplyToAnthropicResponse 聚合响应改写（Anthropic 形状）：content 块里的
// 文本标记还原成 tool_use 块。没有标记时原样返回。
func ApplyToAnthropicResponse(raw []byte) []byte {
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return raw
	}
	blocks, _ := resp["content"].([]any)
	if len(blocks) == 0 {
		return raw
	}
	var text string
	for _, bv := range blocks {
		b, ok := bv.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := b["type"].(string); t == "text" {
			text += b["text"].(string) + "\n"
		}
	}
	content, calls := ParseText(text)
	if len(calls) == 0 {
		return raw
	}
	out := []any{}
	if content != "" {
		out = append(out, map[string]any{"type": "text", "text": content})
	}
	for _, c := range calls {
		input := map[string]any{}
		if json.Unmarshal([]byte(c.Args), &input) != nil {
			input = map[string]any{}
		}
		out = append(out, map[string]any{
			"type": "tool_use",
			"id":   "toolu_" + provider.RandomHex(12),
			"name": c.Name,
			"input": input,
		})
	}
	resp["content"] = out
	resp["stop_reason"] = "tool_use"
	rawOut, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return rawOut
}
