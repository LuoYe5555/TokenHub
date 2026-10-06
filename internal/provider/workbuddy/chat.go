package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// seedModels 上游目录不可用时的兜底模型表（移植自 workbuddy-openai-proxy 配置）。
var seedModels = []provider.ModelInfo{
	{ID: "auto", Name: "Auto"},
	{ID: "deepseek-v4-pro"}, {ID: "deepseek-v4.1-flash"},
	{ID: "claude-sonnet-4.6"}, {ID: "claude-opus-4.6"},
	{ID: "gpt-5.6-luna"}, {ID: "gpt-5.6-terra"}, {ID: "gpt-5.6-sol"}, {ID: "gpt-6-astra"},
	{ID: "gpt-5.5"}, {ID: "gpt-5.4"}, {ID: "gpt-5.3-codex"},
	{ID: "gemini-3.1-pro"}, {ID: "gemini-3.5-flash"}, {ID: "gemini-3.1-flash-image"},
	{ID: "glm-5.3"}, {ID: "glm-5.2"}, {ID: "glm-5.3-flash"},
	{ID: "kimi-k3"}, {ID: "kimi-k2.7"}, {ID: "kimi-k2.6"}, {ID: "kimi-k2.5"},
	{ID: "minimax-m3"}, {ID: "hy3"},
}

func (p *Provider) SeedModels() []provider.ModelInfo { return seedModels }

// Models 动态模型目录（personal/models，5 分钟缓存）。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	if cached, ok := p.modelsCache(acct.Realm); ok {
		return cached, nil
	}
	s := siteFor(acct.Realm, p.cfg.PC(store.PWorkBuddy))
	h := chatHeaders(s, acct)
	h["Accept"] = "application/json"
	var out struct {
		Code int `json:"code"`
		Data struct {
			Models []struct {
				ID              string `json:"id"`
				Name            string `json:"name"`
				Disabled        bool   `json:"disabled"`
				Credits         string `json:"credits"`
				MaxInputTokens  int64  `json:"maxInputTokens"`
				MaxOutputTokens int64  `json:"maxOutputTokens"`
			} `json:"models"`
			Agents map[string]struct {
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if _, err := httpx.DoJSON(ctx, "GET", s.ChatBase+"/console/enterprises/personal/models", h, nil, &out, 30*time.Second); err != nil {
		return nil, err
	}
	if out.Code != 0 || len(out.Data.Models) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "模型目录为空")
	}
	cliModels := map[string]bool{}
	if a, ok := out.Data.Agents["cli"]; ok {
		for _, m := range a.Models {
			cliModels[m] = true
		}
	}
	var list []provider.ModelInfo
	for _, m := range out.Data.Models {
		if m.Disabled || m.ID == "" {
			continue
		}
		if len(cliModels) > 0 && !cliModels[m.ID] {
			continue
		}
		list = append(list, provider.ModelInfo{ID: m.ID, Name: m.Name, MaxInput: m.MaxInputTokens, MaxOutput: m.MaxOutputTokens, Credits: m.Credits})
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "模型目录为空")
	}
	p.setModelsCache(acct.Realm, list)
	return list, nil
}

type modelsCacheEntry struct {
	at    time.Time
	models []provider.ModelInfo
}

var (
	mcacheMu sync.Mutex
	mcache   = map[string]modelsCacheEntry{}
)

func (p *Provider) modelsCache(realm string) ([]provider.ModelInfo, bool) {
	mcacheMu.Lock()
	defer mcacheMu.Unlock()
	e, ok := mcache[realm]
	if !ok || time.Since(e.at) > 5*time.Minute {
		return nil, false
	}
	return e.models, true
}

func (p *Provider) setModelsCache(realm string, models []provider.ModelInfo) {
	mcacheMu.Lock()
	defer mcacheMu.Unlock()
	mcache[realm] = modelsCacheEntry{at: time.Now(), models: models}
}

// ── 对话 ──

func prepareBody(openaiReq []byte, defaultModel string) (map[string]any, error) {
	var src map[string]any
	if err := json.Unmarshal(openaiReq, &src); err != nil {
		return nil, provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	if src == nil {
		src = map[string]any{}
	}
	b := src
	b["stream"] = true
	if m, _ := b["model"].(string); strings.TrimSpace(m) == "" {
		b["model"] = defaultModel
	}
	if v, ok := b["max_completion_tokens"]; ok {
		if _, exists := b["max_tokens"]; !exists {
			b["max_tokens"] = v
		}
		delete(b, "max_completion_tokens")
	}
	if _, ok := b["max_tokens"]; !ok {
		b["max_tokens"] = 16384
	}
	// 工具调用归一化
	if tc, ok := b["tool_choice"]; ok {
		normalized, dropTools := normalizeToolChoice(tc)
		if dropTools {
			delete(b, "tool_choice")
			delete(b, "tools")
			delete(b, "functions")
		} else if normalized != nil {
			b["tool_choice"] = normalized
		} else {
			delete(b, "tool_choice")
		}
	}
	// messages：developer→system；首条必须是 system
	if msgs, ok := b["messages"].([]any); ok && len(msgs) > 0 {
		for _, mv := range msgs {
			if m, ok := mv.(map[string]any); ok && m["role"] == "developer" {
				m["role"] = "system"
			}
		}
		first, _ := msgs[0].(map[string]any)
		if first == nil || first["role"] != "system" {
			b["messages"] = append([]any{map[string]any{"role": "system", "content": "You are a helpful AI assistant."}}, msgs...)
		}
	}
	return b, nil
}

// normalizeToolChoice 返回归一化后的 tool_choice；drop=true 时同时删除 tools。
func normalizeToolChoice(v any) (normalized any, drop bool) {
	switch t := v.(type) {
	case string:
		switch t {
		case "none":
			return nil, true
		default:
			return t, false
		}
	case map[string]any:
		typ, _ := t["type"].(string)
		switch typ {
		case "none":
			return nil, true
		case "auto", "required", "any":
			if typ == "any" {
				return "required", false
			}
			return typ, false
		case "function":
			if fn, ok := t["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok && name != "" {
					return name, false
				}
			}
			return "auto", false
		default:
			return "auto", false
		}
	}
	return "auto", false
}

// Chat 实现 provider.Provider。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	pc := p.cfg.PC(store.PWorkBuddy)
	s := siteFor(acct.Realm, pc)
	body, err := prepareBody(openaiReq, pc.DefaultModel)
	if err != nil {
		sink.Close()
		return err
	}
	raw, _ := json.Marshal(body)
	resp, err := httpx.DoStream(ctx, "POST", s.ChatBase+"/v2/chat/completions", chatHeaders(s, acct), raw, p.cfg.ConnectTimeout())
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	defer func() {
		if resp.Stream != nil {
			resp.Stream.Close()
		}
	}()
	if resp.Status >= 400 {
		sink.Close()
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	// 200 + JSON = 业务错误信封
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		sink.Close()
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	return parseChatSSE(resp.Stream, body, sink)
}

// parseChatSSE 解析上游 OpenAI 风格 SSE（含错误信封帧）。
func parseChatSSE(r io.Reader, sentBody map[string]any, sink provider.Sink) error {
	defer sink.Close()
	sentModel, _ := sentBody["model"].(string)
	firstRoleSent := false
	var finish string
	var usage *provider.Usage
	hasDelta := false

	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return true
		}
		if data == "[DONE]" {
			return false
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Role             string `json:"role"`
					Content          any    `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason any `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
			Code    any    `json:"code"`
			Message string `json:"message"`
			Error   any    `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return true // 忽略不可解析帧
		}
		// 错误信封：无 choices，有 code 或 error
		if len(chunk.Choices) == 0 {
			if chunk.Error != nil || chunk.Code != nil {
				code, msg := extractError(chunk.Code, chunk.Error, chunk.Message)
				sink.StreamError(code, msg)
				return false
			}
			return true
		}
		c := chunk.Choices[0]
		d := c.Delta
		delta := provider.Delta{}
		if d.Role != "" && !firstRoleSent {
			delta.Role = d.Role
			firstRoleSent = true
		}
		if s, ok := d.Content.(string); ok && s != "" {
			delta.Content = s
		}
		delta.Reasoning = d.ReasoningContent
		for _, tc := range d.ToolCalls {
			delta.Tool = &provider.ToolDelta{Index: tc.Index, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments}
			hasDelta = true
			sink.Delta(delta)
			delta = provider.Delta{}
		}
		if delta.Content != "" || delta.Reasoning != "" || delta.Role != "" {
			hasDelta = true
			sink.Delta(delta)
		}
		if fr, ok := c.FinishReason.(string); ok && fr != "" {
			finish = fr
		}
		if chunk.Usage != nil {
			usage = &provider.Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens, Present: true}
		}
		return true
	})
	if err != nil {
		return err
	}
	reason := finish
	if reason == "" {
		if !hasDelta {
			sink.StreamError(0, "上游返回空流")
		}
		reason = "stop"
	}
	_ = sentModel
	sink.Finish(provider.Finish{Reason: reason, Usage: usage})
	return nil
}

func extractError(code any, errObj any, message string) (int64, string) {
	var codeN int64
	switch v := code.(type) {
	case float64:
		codeN = int64(v)
	case string:
		fmt.Sscanf(v, "%d", &codeN)
	}
	switch e := errObj.(type) {
	case string:
		return codeN, e
	case map[string]any:
		if m, ok := e["message"].(string); ok && message == "" {
			message = m
		}
		if c, ok := e["code"].(float64); ok && codeN == 0 {
			codeN = int64(c)
		}
	}
	if message == "" {
		message = "upstream error"
	}
	return codeN, message
}
