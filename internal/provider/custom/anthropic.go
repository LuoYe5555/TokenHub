// anthropic.go Anthropic 兼容上游：OpenAI 请求 → /v1/messages，响应 SSE → 统一 Sink。
package custom

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// chatAnthropic 走 Anthropic /v1/messages（Claude 官方 / 各类中转站）。
func (p *Provider) chatAnthropic(ctx context.Context, acct *store.Account, base string, openaiReq []byte, sink provider.Sink) error {
	chatURL, _ := endpoints(base, "anthropic")
	var src map[string]any
	if err := json.Unmarshal(openaiReq, &src); err != nil {
		sink.Close()
		return provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	body := openAIToAnthropic(src)
	pb, err := json.Marshal(body)
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindClient, 400, "请求转换失败: %v", err)
	}
	resp, err := httpx.DoStream(ctx, "POST", chatURL, headers(acct, "anthropic"), pb, p.cfg.UpstreamTimeout())
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindNetwork, 0, "外部 API 连接失败: %v", err)
	}
	if resp.Status >= 400 {
		sink.Close()
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	return parseAnthropicSSE(resp.Stream, sink)
}

// openAIToAnthropic 把 OpenAI chat 请求转成 Anthropic messages 请求。
func openAIToAnthropic(src map[string]any) map[string]any {
	out := map[string]any{"stream": true}

	// max_tokens：Anthropic 必填
	if mt, ok := src["max_tokens"].(float64); ok && mt > 0 {
		out["max_tokens"] = int64(mt)
	} else {
		out["max_tokens"] = int64(8192)
	}
	if m, ok := src["model"].(string); ok && m != "" {
		out["model"] = m
	}
	if t, ok := src["temperature"].(float64); ok {
		out["temperature"] = t
	}
	if t, ok := src["top_p"].(float64); ok {
		out["top_p"] = t
	}
	// stop → stop_sequences
	switch s := src["stop"].(type) {
	case string:
		if s != "" {
			out["stop_sequences"] = []string{s}
		}
	case []any:
		var ss []string
		for _, v := range s {
			if str, ok := v.(string); ok && str != "" {
				ss = append(ss, str)
			}
		}
		if len(ss) > 0 {
			out["stop_sequences"] = ss
		}
	}

	// tools: OpenAI function → Anthropic input_schema
	if tools, ok := src["tools"].([]any); ok && len(tools) > 0 {
		var atools []map[string]any
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			if tm == nil {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}
			schema, _ := fn["parameters"].(map[string]any)
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			desc, _ := fn["description"].(string)
			atools = append(atools, map[string]any{"name": name, "description": desc, "input_schema": schema})
		}
		if len(atools) > 0 {
			out["tools"] = atools
		}
	}

	// messages：抽 system、映射 tool_calls / tool 结果
	var system []string
	var msgs []map[string]any
	srcMsgs, _ := src["messages"].([]any)
	for _, rm := range srcMsgs {
		mm, _ := rm.(map[string]any)
		if mm == nil {
			continue
		}
		role, _ := mm["role"].(string)
		switch role {
		case "system":
			if s := contentText(mm["content"]); s != "" {
				system = append(system, s)
			}
		case "tool":
			// 工具结果 → user 消息里的 tool_result 块
			msgs = append(msgs, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": stringOr(mm["tool_call_id"]),
					"content":     contentText(mm["content"]),
				}},
			})
		case "assistant":
			blocks := assistantBlocks(mm)
			msgs = append(msgs, map[string]any{"role": "assistant", "content": blocks})
		default: // user
			msgs = append(msgs, map[string]any{"role": "user", "content": contentBlocks(mm["content"])})
		}
	}
	if len(system) > 0 {
		out["system"] = strings.Join(system, "\n\n")
	}
	out["messages"] = msgs
	return out
}

// assistantBlocks assistant 消息：文本 + tool_calls → tool_use 块。
func assistantBlocks(mm map[string]any) []map[string]any {
	var blocks []map[string]any
	if s := contentText(mm["content"]); s != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": s})
	}
	if tcs, ok := mm["tool_calls"].([]any); ok {
		for _, tc := range tcs {
			tcm, _ := tc.(map[string]any)
			if tcm == nil {
				continue
			}
			fn, _ := tcm["function"].(map[string]any)
			if fn == nil {
				continue
			}
			input := map[string]any{}
			if args, ok := fn["arguments"].(string); ok && strings.TrimSpace(args) != "" {
				_ = json.Unmarshal([]byte(args), &input)
			}
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    stringOr(tcm["id"]),
				"name":  stringOr(fn["name"]),
				"input": input,
			})
		}
	}
	if blocks == nil {
		blocks = []map[string]any{{"type": "text", "text": ""}}
	}
	return blocks
}

// contentBlocks 用户消息 content：字符串或分段数组 → Anthropic content 块。
func contentBlocks(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	parts, ok := v.([]any)
	if !ok {
		return ""
	}
	var blocks []map[string]any
	hasText := false
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		switch pm["type"] {
		case "text":
			if s, _ := pm["text"].(string); s != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": s})
				hasText = true
			}
		case "image_url":
			// data URL → base64 块；http(s) URL → url 块；其余跳过
			iu, _ := pm["image_url"].(map[string]any)
			u, _ := iu["url"].(string)
			if u == "" {
				continue
			}
			if strings.HasPrefix(u, "data:") {
				media, b64, ok := splitDataURL(u)
				if !ok {
					continue
				}
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": media,
						"data":       b64,
					},
				})
			} else if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				blocks = append(blocks, map[string]any{
					"type":   "image",
					"source": map[string]any{"type": "url", "url": u},
				})
			}
		}
	}
	if !hasText && blocks == nil {
		return ""
	}
	return blocks
}

// splitDataURL "data:image/png;base64,xxxx" → ("image/png", "xxxx", true)。
func splitDataURL(u string) (media, b64 string, ok bool) {
	rest := strings.TrimPrefix(u, "data:")
	semi := strings.Index(rest, ";")
	comma := strings.Index(rest, ",")
	if semi < 0 || comma < semi {
		return "", "", false
	}
	return rest[:semi], rest[comma+1:], true
}

// contentText 任意 content（字符串/分段数组）摊平成纯文本。
func contentText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	parts, ok := v.([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		if pm["type"] == "text" {
			if s, _ := pm["text"].(string); s != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

func stringOr(v any) string {
	s, _ := v.(string)
	return s
}

// parseAnthropicSSE 解析 Anthropic messages SSE → 统一 Sink。
func parseAnthropicSSE(r io.Reader, sink provider.Sink) error {
	defer sink.Close()
	// index → 是否已在 content_block_start 里发过工具头
	toolStarted := map[int]bool{}
	hasDelta := false
	finishReason := ""
	promptTokens, completionTokens := int64(0), int64(0)

	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return true
		}
		var e struct {
			Type  string `json:"type"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
			Message *struct {
				Usage *struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Index        int `json:"index"`
			ContentBlock *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &e) != nil {
			return true
		}
		switch e.Type {
		case "error":
			msg := "外部 API 上游错误"
			if e.Error != nil && e.Error.Message != "" {
				msg = e.Error.Message
			}
			sink.StreamError(0, msg)
			return false
		case "message_start":
			if e.Message != nil && e.Message.Usage != nil {
				promptTokens = e.Message.Usage.InputTokens
				if e.Message.Usage.OutputTokens > 0 {
					completionTokens = e.Message.Usage.OutputTokens
				}
			}
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" {
				toolStarted[e.Index] = true
				hasDelta = true
				sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: e.Index, ID: e.ContentBlock.ID, Name: e.ContentBlock.Name}})
			}
		case "content_block_delta":
			if e.Delta == nil {
				return true
			}
			switch e.Delta.Type {
			case "text_delta":
				if e.Delta.Text != "" {
					hasDelta = true
					sink.Delta(provider.Delta{Content: e.Delta.Text})
				}
			case "thinking_delta":
				if e.Delta.Thinking != "" {
					hasDelta = true
					sink.Delta(provider.Delta{Reasoning: e.Delta.Thinking})
				}
			case "input_json_delta":
				if toolStarted[e.Index] && e.Delta.PartialJSON != "" {
					hasDelta = true
					sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: e.Index, Args: e.Delta.PartialJSON}})
				}
			}
		case "message_delta":
			if e.Delta != nil && e.Delta.StopReason != "" {
				finishReason = mapStopReason(e.Delta.StopReason)
			}
			if e.Usage != nil && e.Usage.OutputTokens > 0 {
				completionTokens = e.Usage.OutputTokens
			}
		case "message_stop":
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	usage := &provider.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		Present:          promptTokens > 0 || completionTokens > 0,
	}
	if !hasDelta && !usage.Present {
		sink.StreamError(0, "外部 API 返回空流，请重试")
		return nil
	}
	sink.Finish(provider.Finish{Reason: finishReason, Usage: usage})
	return nil
}

// mapStopReason Anthropic stop_reason → OpenAI finish_reason。
func mapStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default: // end_turn / stop_sequence / ...
		return "stop"
	}
}
