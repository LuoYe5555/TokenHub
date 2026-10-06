// Package gateway OpenAI / Anthropic 兼容网关。
package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/provider"
)

// ── OpenAI 流式 Sink ──

// OpenAIStreamSink 把增量写成 OpenAI chat.completion.chunk SSE。
type OpenAIStreamSink struct {
	w        io.Writer
	flusher  http.Flusher
	mu       sync.Mutex
	id       string
	created  int64
	model    string
	roleSent bool
	wrote    bool
}

func NewOpenAIStreamSink(w io.Writer, fl http.Flusher, model string) *OpenAIStreamSink {
	return &OpenAIStreamSink{w: w, flusher: fl, id: "chatcmpl-" + provider.RandomHex(12), created: time.Now().Unix(), model: model}
}

// Wrote 是否已向客户端写出任何字节（决定出错时能否换回 HTTP 错误码）。
func (s *OpenAIStreamSink) Wrote() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

func (s *OpenAIStreamSink) write(payload map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote = true
	payload["id"] = s.id
	payload["object"] = "chat.completion.chunk"
	payload["created"] = s.created
	payload["model"] = s.model
	raw, _ := json.Marshal(payload)
	fmt.Fprintf(s.w, "data: %s\n\n", raw)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *OpenAIStreamSink) Delta(d provider.Delta) {
	choice := map[string]any{"index": 0, "delta": map[string]any{}}
	delta := choice["delta"].(map[string]any)
	if d.Role != "" && !s.roleSent {
		delta["role"] = d.Role
		s.roleSent = true
	}
	if d.Content != "" {
		delta["content"] = d.Content
	}
	if d.Reasoning != "" {
		delta["reasoning_content"] = d.Reasoning
	}
	if d.Tool != nil {
		tc := map[string]any{"index": d.Tool.Index, "type": "function", "function": map[string]any{}}
		fn := tc["function"].(map[string]any)
		if d.Tool.ID != "" {
			tc["id"] = d.Tool.ID
		}
		if d.Tool.Name != "" {
			fn["name"] = d.Tool.Name
		}
		if d.Tool.Args != "" {
			fn["arguments"] = d.Tool.Args
		}
		delta["tool_calls"] = []any{tc}
	}
	if len(delta) == 0 {
		return
	}
	s.write(map[string]any{"choices": []any{choice}})
}

func (s *OpenAIStreamSink) Finish(f provider.Finish) {
	payload := map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finishReasonOrStop(f.Reason)}},
	}
	if f.Usage != nil && f.Usage.Present {
		payload["usage"] = f.Usage
	}
	s.write(payload)
	s.writeDone()
}

func (s *OpenAIStreamSink) StreamError(code int64, msg string) {
	s.write(map[string]any{
		"choices": []any{},
		"error":   map[string]any{"message": msg, "code": code},
	})
	s.writeDone()
}

func (s *OpenAIStreamSink) writeDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *OpenAIStreamSink) Close() {}

func finishReasonOrStop(r string) string {
	if r == "" {
		return "stop"
	}
	return r
}

// ── 聚合 Sink（非流式 OpenAI） ──

type toolAcc struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// AggregateSink 聚合整条流为一份 OpenAI chat.completion。
type AggregateSink struct {
	mu       sync.Mutex
	id       string
	created  int64
	model    string
	role     string
	content  strings.Builder
	reason   strings.Builder
	tools    map[int]*toolAcc
	toolIdxs []int
	finish   string
	usage    *provider.Usage
	errCode  int64
	errMsg   string
}

func NewAggregateSink(model string) *AggregateSink {
	return &AggregateSink{id: "chatcmpl-" + provider.RandomHex(12), created: time.Now().Unix(), model: model, tools: map[int]*toolAcc{}}
}

func (s *AggregateSink) Delta(d provider.Delta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Role != "" && s.role == "" {
		s.role = d.Role
	}
	if d.Content != "" {
		s.content.WriteString(d.Content)
	}
	if d.Reasoning != "" {
		s.reason.WriteString(d.Reasoning)
	}
	if d.Tool != nil {
		t, ok := s.tools[d.Tool.Index]
		if !ok {
			t = &toolAcc{index: d.Tool.Index}
			s.tools[d.Tool.Index] = t
			s.toolIdxs = append(s.toolIdxs, d.Tool.Index)
		}
		if d.Tool.ID != "" {
			t.id = d.Tool.ID
		}
		if d.Tool.Name != "" {
			t.name = d.Tool.Name
		}
		if d.Tool.Args != "" {
			t.args.WriteString(d.Tool.Args)
		}
	}
}

func (s *AggregateSink) Finish(f provider.Finish) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Reason != "" {
		s.finish = f.Reason
	}
	if f.Usage != nil {
		s.usage = f.Usage
	}
}

func (s *AggregateSink) StreamError(code int64, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errCode = code
	s.errMsg = msg
}

func (s *AggregateSink) Close() {}

// Result 输出 OpenAI chat.completion；有流内错误时返回 error。
func (s *AggregateSink) Result() (json.RawMessage, *provider.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errMsg != "" {
		return nil, &provider.Error{Kind: provider.KindStream, Msg: s.errMsg}
	}
	message := map[string]any{"role": "assistant", "content": s.content.String()}
	if r := s.reason.String(); r != "" {
		message["reasoning_content"] = r
	}
	if len(s.toolIdxs) > 0 {
		sortInts(s.toolIdxs)
		calls := make([]any, 0, len(s.toolIdxs))
		for _, idx := range s.toolIdxs {
			t := s.tools[idx]
			calls = append(calls, map[string]any{
				"id": t.id, "type": "function",
				"function": map[string]any{"name": t.name, "arguments": t.args.String()},
			})
		}
		message["tool_calls"] = calls
	}
	resp := map[string]any{
		"id":      s.id,
		"object":  "chat.completion",
		"created": s.created,
		"model":   s.model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReasonOrStop(s.finish)}},
	}
	if s.usage != nil && s.usage.Present {
		resp["usage"] = s.usage
	}
	raw, _ := json.Marshal(resp)
	return raw, nil
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ── Anthropic 流式 Sink ──

// AnthropicStreamSink 把增量写成 Anthropic message SSE（供 Claude Code 等客户端）。
type AnthropicStreamSink struct {
	w          io.Writer
	flusher    http.Flusher
	mu         sync.Mutex
	model      string
	started    bool
	blockOpen  bool
	blockIdx   int
	toolIdxMap map[int]int // openai tool index → anthropic block index
	usage      *provider.Usage
	wrote      bool
}

func NewAnthropicStreamSink(w io.Writer, fl http.Flusher, model string) *AnthropicStreamSink {
	return &AnthropicStreamSink{w: w, flusher: fl, model: model, toolIdxMap: map[int]int{}}
}

func (s *AnthropicStreamSink) ev(event string, payload map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote = true
	payload["type"] = event
	raw, _ := json.Marshal(payload)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, raw)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// Wrote 是否已向客户端写出任何字节。
func (s *AnthropicStreamSink) Wrote() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

func (s *AnthropicStreamSink) ensureStart() {
	if s.started {
		return
	}
	s.started = true
	s.ev("message_start", map[string]any{
		"message": map[string]any{
			"id": "msg_" + provider.RandomHex(12), "type": "message", "role": "assistant",
			"model": s.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// openBlock 结束当前块并开启新块，content_block 里带初始字段。
func (s *AnthropicStreamSink) openBlock(content map[string]any) int {
	s.ensureStart()
	if s.blockOpen {
		s.closeBlock()
	}
	idx := s.blockIdx
	s.blockIdx++
	s.blockOpen = true
	s.ev("content_block_start", map[string]any{"index": idx, "content_block": content})
	return idx
}

func (s *AnthropicStreamSink) closeBlock() {
	if !s.blockOpen {
		return
	}
	s.blockOpen = false
	s.ev("content_block_stop", map[string]any{"index": s.blockIdx - 1})
}

func (s *AnthropicStreamSink) delta(fields map[string]any, index int) {
	s.ev("content_block_delta", map[string]any{"index": index, "delta": fields})
}

func (s *AnthropicStreamSink) currentOrOpen(content map[string]any) int {
	if s.blockOpen {
		return s.blockIdx - 1
	}
	return s.openBlock(content)
}

func (s *AnthropicStreamSink) Delta(d provider.Delta) {
	switch {
	case d.Tool != nil:
		if blockIdx, ok := s.toolIdxMap[d.Tool.Index]; ok {
			if d.Tool.Args != "" {
				s.delta(map[string]any{"type": "input_json_delta", "partial_json": d.Tool.Args}, blockIdx)
			}
			return
		}
		blockIdx := s.openBlock(map[string]any{"type": "tool_use", "id": d.Tool.ID, "name": d.Tool.Name, "input": map[string]any{}})
		s.toolIdxMap[d.Tool.Index] = blockIdx
		if d.Tool.Args != "" {
			s.delta(map[string]any{"type": "input_json_delta", "partial_json": d.Tool.Args}, blockIdx)
		}
	case d.Reasoning != "":
		idx := s.currentOrOpen(map[string]any{"type": "thinking", "thinking": ""})
		s.delta(map[string]any{"type": "thinking_delta", "thinking": d.Reasoning}, idx)
	case d.Content != "":
		idx := s.currentOrOpen(map[string]any{"type": "text", "text": ""})
		s.delta(map[string]any{"type": "text_delta", "text": d.Content}, idx)
	}
}

func (s *AnthropicStreamSink) Finish(f provider.Finish) {
	s.closeBlock()
	if f.Usage != nil {
		s.usage = f.Usage
	}
	var outTokens, inTokens int64
	if s.usage != nil {
		outTokens = s.usage.CompletionTokens
		inTokens = s.usage.PromptTokens
	}
	s.ensureStart()
	s.ev("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": anthropicStop(f.Reason), "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outTokens, "input_tokens": inTokens},
	})
	s.ev("message_stop", map[string]any{})
}

func (s *AnthropicStreamSink) StreamError(code int64, msg string) {
	s.ensureStart()
	s.ev("error", map[string]any{"error": map[string]any{"type": "api_error", "message": msg}})
}

func (s *AnthropicStreamSink) Close() {}

func anthropicStop(r string) string {
	switch r {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

// ── Anthropic 非流式聚合 ──

// AnthropicAggregateSink 聚合为 Anthropic message JSON。
type AnthropicAggregateSink struct {
	inner *AggregateSink
	model string
}

func NewAnthropicAggregateSink(model string) *AnthropicAggregateSink {
	return &AnthropicAggregateSink{inner: NewAggregateSink(model), model: model}
}

func (s *AnthropicAggregateSink) Delta(d provider.Delta)        { s.inner.Delta(d) }
func (s *AnthropicAggregateSink) Finish(f provider.Finish)      { s.inner.Finish(f) }
func (s *AnthropicAggregateSink) StreamError(c int64, m string) { s.inner.StreamError(c, m) }
func (s *AnthropicAggregateSink) Close()                        {}

func (s *AnthropicAggregateSink) Result() (json.RawMessage, *provider.Error) {
	raw, err := s.inner.Result()
	if err != nil {
		return nil, err
	}
	var openaiResp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *provider.Usage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &openaiResp)
	content := []any{}
	if len(openaiResp.Choices) > 0 {
		msg := openaiResp.Choices[0].Message
		if msg.ReasoningContent != "" {
			content = append(content, map[string]any{"type": "thinking", "thinking": msg.ReasoningContent, "signature": ""})
		}
		if msg.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": msg.Content})
		}
		for _, tc := range msg.ToolCalls {
			input := map[string]any{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
		}
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if openaiResp.Usage != nil {
		usage["input_tokens"] = openaiResp.Usage.PromptTokens
		usage["output_tokens"] = openaiResp.Usage.CompletionTokens
	}
	stop := "end_turn"
	if len(openaiResp.Choices) > 0 {
		stop = anthropicStop(openaiResp.Choices[0].FinishReason)
	}
	out := map[string]any{
		"id": "msg_" + provider.RandomHex(12), "type": "message", "role": "assistant",
		"model": s.model, "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": usage,
	}
	rawOut, _ := json.Marshal(out)
	return rawOut, nil
}
