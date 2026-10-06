package zcode

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/provider"
)

// planShape 客户端真实流量捕获的请求形状（system 提示词数组 + reminder + metadata）。
type planShape struct {
	System       []map[string]any `json:"system"`
	ReminderText string           `json:"reminderText"`
	Metadata     map[string]any   `json:"metadata"`
}

var (
	shapeOnce  sync.Once
	shapeValue *planShape
)

func loadShape() *planShape {
	shapeOnce.Do(func() {
		var s planShape
		if err := json.Unmarshal(shapeJSON, &s); err == nil && len(s.System) > 0 {
			shapeValue = &s
		}
	})
	return shapeValue
}

var dateRe = regexp.MustCompile(`Today's date is [^.]+\.`)

// applyShape 注入客户端请求形状（风控通过票，移植自 CreditDaddy buildPlanRequest）。
func applyShape(body map[string]any, mode string) {
	s := loadShape()
	if s == nil || mode == "off" {
		return
	}
	switch mode {
	case "prepend":
		existing := collectSystem(body)
		body["system"] = append(append([]map[string]any{}, s.System...), existing...)
	default: // overwrite
		body["system"] = s.System
	}
	// 首条 user 消息前插入 currentDate 提醒块
	reminder := s.ReminderText
	if reminder != "" {
		reminder = dateRe.ReplaceAllString(reminder, "Today's date is "+time.Now().Format("2006-01-02")+".")
		msgs := messagesOf(body)
		if len(msgs) > 0 {
			first, _ := msgs[0].(map[string]any)
			if first != nil && first["role"] == "user" {
				if !contentContains(first, "<system-reminder>") {
					block := map[string]any{"type": "text", "text": reminder}
					switch c := first["content"].(type) {
					case []any:
						first["content"] = append([]any{block}, c...)
					case string:
						first["content"] = []any{block, map[string]any{"type": "text", "text": c}}
					default:
						first["content"] = []any{block, map[string]any{"type": "text", "text": "."}}
					}
				}
			} else {
				msgs = append([]any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": reminder},
					map[string]any{"type": "text", "text": "."},
				}}}, msgs...)
				body["messages"] = msgs
			}
		} else {
			body["messages"] = []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": reminder},
				map[string]any{"type": "text", "text": "."},
			}}}
		}
	}
	body["metadata"] = map[string]any{
		"user_id": fmt.Sprintf(`{"account_uuid":"","session_id":"ses_%s"}`, provider.RandomHex(8)),
	}
}

func messagesOf(body map[string]any) []any {
	switch m := body["messages"].(type) {
	case []any:
		return m
	case []map[string]any: // 兜底：构造方直接存了 map 切片
		out := make([]any, len(m))
		for i, v := range m {
			out[i] = v
		}
		return out
	}
	return nil
}

// blocksToAny 把 block 切片转成 []any（JSON 等价，但类型断言兼容）。
func blocksToAny(in []map[string]any) []any {
	if in == nil {
		return nil
	}
	out := make([]any, len(in))
	for i, b := range in {
		out[i] = b
	}
	return out
}

func contentContains(msg map[string]any, substr string) bool {
	switch c := msg["content"].(type) {
	case string:
		return strings.Contains(c, substr)
	case []any:
		for _, part := range c {
			if m, ok := part.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && strings.Contains(t, substr) {
					return true
				}
			}
		}
	}
	return false
}

// collectSystem 取出现有 system 参数（string 或 blocks）为 blocks。
func collectSystem(body map[string]any) []map[string]any {
	switch s := body["system"].(type) {
	case string:
		if s == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": s}}
	case []any:
		var out []map[string]any
		for _, b := range s {
			if m, ok := b.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// ── OpenAI 请求 → Anthropic 请求 ──

// BuildAnthropicFromOpenAI 把 OpenAI chat 请求转换为 Anthropic messages 请求。
func BuildAnthropicFromOpenAI(openaiReq []byte, defaultModel string, defaultMaxTokens int64) (map[string]any, error) {
	var src map[string]any
	if err := json.Unmarshal(openaiReq, &src); err != nil {
		return nil, provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	if src == nil {
		src = map[string]any{}
	}
	model, _ := src["model"].(string)
	if strings.TrimSpace(model) == "" {
		model = defaultModel
	}
	maxTokens := defaultMaxTokens
	if v, ok := src["max_tokens"].(float64); ok && v > 0 {
		maxTokens = int64(v)
	}
	if v, ok := src["max_completion_tokens"].(float64); ok && v > 0 && maxTokens == defaultMaxTokens {
		maxTokens = int64(v)
	}
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	out := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if t, ok := src["temperature"].(float64); ok {
		out["temperature"] = t
	}
	if t, ok := src["top_p"].(float64); ok {
		out["top_p"] = t
	}
	switch stop := src["stop"].(type) {
	case string:
		if stop != "" {
			out["stop_sequences"] = []string{stop}
		}
	case []any:
		out["stop_sequences"] = stop
	}

	var system []map[string]any
	var messages []map[string]any
	appendMessage := func(role string, content []map[string]any) {
		if len(content) == 0 {
			return
		}
		// 相邻 tool_result 合并进同一条 user 消息
		if role == "user" && len(messages) > 0 && messages[len(messages)-1]["role"] == "user" {
			prev := messages[len(messages)-1]["content"].([]map[string]any)
			messages[len(messages)-1]["content"] = append(prev, content...)
			return
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	toTextBlock := func(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

	if msgs, ok := src["messages"].([]any); ok {
		for _, mv := range msgs {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			switch role {
			case "system", "developer":
				switch c := m["content"].(type) {
				case string:
					if c != "" {
						system = append(system, toTextBlock(c))
					}
				case []any:
					for _, part := range c {
						if b, ok := part.(map[string]any); ok {
							if t, ok := b["text"].(string); ok && t != "" {
								system = append(system, toTextBlock(t))
							}
						}
					}
				}
			case "user":
				var content []map[string]any
				switch c := m["content"].(type) {
				case string:
					if c != "" {
						content = append(content, toTextBlock(c))
					}
				case []any:
					for _, part := range c {
						b, ok := part.(map[string]any)
						if !ok {
							continue
						}
						typ, _ := b["type"].(string)
						switch typ {
						case "text":
							if t, _ := b["text"].(string); t != "" {
								content = append(content, toTextBlock(t))
							}
						case "image_url":
							if iu, _ := b["image_url"].(map[string]any); iu != nil {
								if u, _ := iu["url"].(string); u != "" {
									if blk := imageBlock(u); blk != nil {
										content = append(content, blk)
									}
								}
							}
						}
					}
				}
				appendMessage("user", content)
			case "assistant":
				var content []map[string]any
				switch c := m["content"].(type) {
				case string:
					if c != "" {
						content = append(content, toTextBlock(c))
					}
				case []any:
					for _, part := range c {
						if b, ok := part.(map[string]any); ok {
							if t, _ := b["text"].(string); t != "" {
								content = append(content, toTextBlock(t))
							}
						}
					}
				}
				if tcs, ok := m["tool_calls"].([]any); ok {
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
						args, _ := fn["arguments"].(string)
						input := map[string]any{}
						if strings.TrimSpace(args) != "" {
							_ = json.Unmarshal([]byte(args), &input)
						}
						id, _ := c["id"].(string)
						content = append(content, map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
					}
				}
				appendMessage("assistant", content)
			case "tool":
				// tool 结果 → user 消息里的 tool_result
				result := ""
				switch c := m["content"].(type) {
				case string:
					result = c
				case []any:
					var sb strings.Builder
					for _, part := range c {
						if b, ok := part.(map[string]any); ok {
							if t, ok := b["text"].(string); ok {
								sb.WriteString(t)
							}
						}
					}
					result = sb.String()
				}
				tid, _ := m["tool_call_id"].(string)
				appendMessage("user", []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": tid,
					"content":     result,
				}})
			}
		}
	}
	if len(system) > 0 {
		out["system"] = blocksToAny(system)
	}
	// 消息内容统一转成 []any：applyShape/messagesOf 断言的是 []any，
	// 若保留 []map[string]any 会导致断言失败 → 注入 reminder 时把整段对话丢掉
	for _, msg := range messages {
		if c, ok := msg["content"].([]map[string]any); ok {
			msg["content"] = blocksToAny(c)
		}
	}
	out["messages"] = blocksToAny(messages)

	// tools
	if tools, ok := src["tools"].([]any); ok && len(tools) > 0 {
		var atools []map[string]any
		for _, tv := range tools {
			t, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := t["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			desc, _ := fn["description"].(string)
			schema, _ := fn["parameters"].(map[string]any)
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			atools = append(atools, map[string]any{"name": name, "description": desc, "input_schema": schema})
		}
		if len(atools) > 0 {
			out["tools"] = atools
		}
	}
	if tc, ok := src["tool_choice"]; ok {
		switch t := tc.(type) {
		case string:
			switch t {
			case "required":
				out["tool_choice"] = map[string]any{"type": "any"}
			case "auto":
				out["tool_choice"] = map[string]any{"type": "auto"}
			case "none":
				delete(out, "tools")
			}
		case map[string]any:
			typ, _ := t["type"].(string)
			switch typ {
			case "required":
				out["tool_choice"] = map[string]any{"type": "any"}
			case "function":
				if fn, ok := t["function"].(map[string]any); ok {
					if name, _ := fn["name"].(string); name != "" {
						out["tool_choice"] = map[string]any{"type": "tool", "name": name}
					}
				}
			case "none":
				delete(out, "tools")
			}
		}
	}
	return out, nil
}

// imageBlock data:image/png;base64,xxx → Anthropic image block。
func imageBlock(dataURL string) map[string]any {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return nil // 远程 URL 上游不一定支持，跳过
	}
	rest := strings.TrimPrefix(dataURL, prefix)
	semi := strings.Index(rest, ";base64,")
	if semi < 0 {
		return nil
	}
	mediaType := rest[:semi]
	data := rest[semi+len(";base64,"):]
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "base64", "media_type": mediaType, "data": data},
	}
}

// ── Anthropic SSE → Sink ──

// StreamAnthropic 解析 Anthropic SSE 到 sink（stop_reason 映射为 OpenAI 语义）。
func StreamAnthropic(r io.Reader, sink provider.Sink) error {
	type toolBlock struct {
		toolIdx int
	}
	blockTool := map[int]toolBlock{}
	nextToolIdx := 0
	var usage *provider.Usage
	var stopReason string
	var streamErr *provider.Error
	firstRoleSent := false

	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		if ev.Event == "ping" || ev.Data == "" {
			return true
		}
		var data map[string]any
		if json.Unmarshal([]byte(ev.Data), &data) != nil {
			return true
		}
		typ, _ := data["type"].(string)
		if typ == "" {
			typ = ev.Event // 兼容 data 缺省 type 字段的实现
		}
		switch typ {
		case "message_start":
			if msg, ok := data["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					usage = &provider.Usage{Present: true}
					usage.PromptTokens = toInt(u["input_tokens"])
					usage.CompletionTokens = toInt(u["output_tokens"])
				}
			}
		case "content_block_start":
			idx := int(toInt(data["index"]))
			cb, _ := data["content_block"].(map[string]any)
			if cb != nil && cb["type"] == "tool_use" {
				id, _ := cb["id"].(string)
				name, _ := cb["name"].(string)
				blockTool[idx] = toolBlock{toolIdx: nextToolIdx}
				sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: nextToolIdx, ID: id, Name: name}})
				nextToolIdx++
			}
		case "content_block_delta":
			idx := int(toInt(data["index"]))
			d, _ := data["delta"].(map[string]any)
			if d == nil {
				return true
			}
			switch d["type"] {
			case "text_delta":
				if t, _ := d["text"].(string); t != "" {
					delta := provider.Delta{Content: t}
					if !firstRoleSent {
						delta.Role = "assistant"
						firstRoleSent = true
					}
					sink.Delta(delta)
				}
			case "thinking_delta":
				if t, _ := d["thinking"].(string); t != "" {
					sink.Delta(provider.Delta{Reasoning: t})
				}
			case "input_json_delta":
				if tb, ok := blockTool[idx]; ok {
					if t, _ := d["partial_json"].(string); t != "" {
						sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: tb.toolIdx, Args: t}})
					}
				}
			}
		case "message_delta":
			if d, ok := data["delta"].(map[string]any); ok {
				stopReason = mapStopReason(d["stop_reason"])
			}
			if u, ok := data["usage"].(map[string]any); ok {
				if usage == nil {
					usage = &provider.Usage{Present: true}
				}
				if out := toInt(u["output_tokens"]); out > 0 {
					usage.CompletionTokens = out
				}
				if in := toInt(u["input_tokens"]); in > 0 {
					usage.PromptTokens = in
				}
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
		case "message_stop":
			return false
		case "error":
			e, _ := data["error"].(map[string]any)
			msg := "upstream error"
			if e != nil {
				if m, ok := e["message"].(string); ok && m != "" {
					msg = m
				}
			}
			streamErr = &provider.Error{Kind: provider.KindStream, Msg: msg}
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	if streamErr != nil {
		return streamErr
	}
	if stopReason == "" {
		stopReason = "stop"
	}
	sink.Finish(provider.Finish{Reason: stopReason, Usage: usage})
	return nil
}

func mapStopReason(v any) string {
	s, _ := v.(string)
	switch s {
	case "end_turn", "":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	}
	return 0
}
