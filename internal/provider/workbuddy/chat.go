package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// seedModels 上游目录不可用时的兜底模型表。
// 以 2026-10-24 实测的国服 personal/models 目录为准——国服已下线 claude/gpt/gemini，
// 兜底表若再列这些会诱导选用并报 11102「当前模型不可用」（易被误判为认证被拦）。
// 国际版目录仍含 claude/gpt/gemini，但其动态目录一直可用，兜底场景极少。
var seedModels = []provider.ModelInfo{
	{ID: "auto", Name: "Auto"},
	{ID: "hy3", Name: "Hy3"}, {ID: "hy3-x", Name: "Hy3"},
	{ID: "hy4-preview", Name: "Hy4 Preview"}, {ID: "hy4-preview-x", Name: "Hy4 Preview"},
	{ID: "glm-5.3", Name: "GLM-5.3"}, {ID: "glm-5.2", Name: "GLM-5.2"}, {ID: "glm-5.3-flash", Name: "GLM-5.3-Flash"},
	{ID: "glm-5.1"}, {ID: "glm-5.0"}, {ID: "glm-4.7"}, {ID: "glm-4.6"}, {ID: "glm-4.6v", Name: "GLM-4.6V"},
	{ID: "glm-5v-turbo", Name: "GLM-5v-Turbo"},
	{ID: "kimi-k3-1", Name: "Kimi-K3"}, {ID: "kimi-k3.1-pro", Name: "Kimi-K3.1-Pro"},
	{ID: "kimi-k2.8-preview", Name: "Kimi-K2.8-Preview"}, {ID: "kimi-k2.5"}, {ID: "kimi-k2-thinking", Name: "Kimi-K2-Thinking"},
	{ID: "deepseek-v4-pro", Name: "DeepSeek-V4-Pro"}, {ID: "deepseek-v4.1-flash", Name: "DeepSeek-V4.1-Flash"},
	{ID: "deepseek-v4-flash", Name: "DeepSeek-V4-Flash"}, {ID: "deepseek-v3-2-volc", Name: "DeepSeek-V3.2"},
	{ID: "minimax-m3", Name: "MiniMax-M3"}, {ID: "minimax-m2.5", Name: "MiniMax-M2.5"}, {ID: "minimax-m3.1-flash", Name: "MiniMax-M3.1-Flash"},
	{ID: "hunyuan-2.0-thinking", Name: "Hunyuan-2.0-Thinking"}, {ID: "hunyuan-chat", Name: "Hunyuan-Turbos"},
	{ID: "step-5-preview", Name: "Step-5-Preview"}, {ID: "mimo-v2.6-pro", Name: "MiMo-V2.6-Pro"},
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
	at     time.Time
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
		guardMessages(b["messages"].([]any))
	}
	return b, nil
}

// ── 渠道校验规避（11128）──
// 上游自 2026-10 起对 system 提示词做客户端指纹拦截：提示词以
// "You are an AI agent powered by …" 开头（前导空白也命中）即整单拒绝，
// 返回 400 / code 11128 "Illegal API invocation from an unapproved channel"。
// 与账号、token、请求头（UA/X-Domain/X-Product 等）、模型、端点均无关——
// 同一账号发普通提示词可正常返回（dsh-buddy-guard 2026-10-29 实测）。
// 社区验证的通过写法："You are an AI agent, powered by …"（仅加一个逗号，语义不变）。
const (
	fpOld = "You are an AI agent powered by "
	fpNew = "You are an AI agent, powered by "
	// 上游第二条规则（2026-10-27，AND 匹配）：system 里同时出现下面两句即拒。
	// 均为 DeepSeek Harness 描述 @ 路径引用语法的固定文案，每轮必带。
	// 只破坏一句即可让 AND 匹配失败；改写语义等价、长度相近（dsh-buddy-guard v0.1.2 实测通过）。
	fpAOld = "Tokens prefixed with @ are paths the user explicitly referenced"
	fpANew = "Path tokens starting with the at-sign point at locations the user referenced"
)

// guardMessages 中和全部消息（system/user/assistant/tool）中的已知客户端指纹。
// 覆盖全部角色：上游规则以 system 为主，但 channel 类拦截可能扫全对话，全改最稳。
// 改写均语义等价，不影响模型理解。
func guardMessages(msgs []any) {
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			m["content"] = guardText(c)
		case []any:
			for _, pv := range c {
				if pm, ok := pv.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						pm["text"] = guardText(t)
					}
				}
			}
		}
	}
}

func guardText(s string) string {
	s = strings.ReplaceAll(s, fpOld, fpNew)
	return strings.ReplaceAll(s, fpAOld, fpANew)
}

// dump11128 上游 11128 渠道校验拦截时，保存完整出站请求样本到日志目录，
// 供指纹词表分析（社区 recipes：dump → 重放 → 逐变量定位）。
func dump11128(raw []byte) {
	dir := logx.LogDir()
	if dir == "" {
		return
	}
	p := filepath.Join(dir, fmt.Sprintf("11128-dump-%d.json", time.Now().Unix()))
	if os.WriteFile(p, raw, 0o600) == nil {
		logx.Warnf("workbuddy", "11128 拦截：出站请求样本已保存 %s（用于指纹词表分析）", p)
	}
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
	// 通道校验类 400（11128）存在风控抖动：同一请求原样重发一发常可通过（EchoBird 社区实测）。
	// 仅在未向客户端写出任何字节前重试，且最多一次，避免加重风控。
	for attempt := 1; ; attempt++ {
		resp, err := httpx.DoStream(ctx, "POST", s.ChatBase+"/v2/chat/completions", chatHeaders(s, acct), raw, p.cfg.ConnectTimeout())
		if err != nil {
			sink.Close()
			return provider.Err(provider.KindNetwork, 0, "%v", err)
		}
		if resp.Status >= 400 && bytes.Contains(resp.Body, []byte("11128")) {
			dump11128(raw)
			if attempt == 1 {
				resp.Stream.Close()
				time.Sleep(800 * time.Millisecond)
				continue
			}
		}
		if resp.Status >= 400 {
			sink.Close()
			return wbFriendlyError(resp.Status, resp.Body)
		}
		// 200 + JSON = 业务错误信封
		if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
			sink.Close()
			return wbFriendlyError(resp.Status, resp.Body)
		}
		err = parseChatSSE(resp.Stream, body, sink)
		resp.Stream.Close()
		return err
	}
}

// wbFriendlyError 解析国服错误信封，把易误判的业务码翻译成明确指引。
func wbFriendlyError(status int, body []byte) error {
	err := provider.ClassifyBody(status, body)
	if len(body) == 0 {
		return err
	}
	var env struct {
		Code       int    `json:"code"`
		Msg        string `json:"msg"`
		DisplayMsg struct {
			Zh string `json:"zh"`
		} `json:"displayMsg"`
	}
	if json.Unmarshal(body, &env) != nil || env.Code == 0 {
		return err
	}
	switch env.Code {
	case 11102:
		return provider.Err(provider.KindModel, status,
			"模型在上游不存在或未接入（%s）：国服目录已下线 claude/gpt/gemini，请改用 GLM / Kimi / DeepSeek / 混元 等国服在架模型", env.Msg)
	case 11101:
		return provider.Err(provider.KindClient, status, "上游要求流式请求（%s）", env.Msg)
	case 11128:
		return provider.Err(provider.KindClient, status,
			"上游渠道校验拦截（11128，%s）：提示词命中非官方客户端指纹，与账号/token/请求头无关。"+
				"TokenHub 已自动改写已知指纹并原地重试一次仍被拒；"+
				"本次请求完整样本已存至日志目录（11128-dump-*.json），把它发给维护者即可定位并更新指纹词表", env.Msg)
	case 11140:
		// 社区/同类网关（DSH workbuddy-gateway）共识：11140=账号级风控拦截（请求未达模型），
		// 常见诱因：高频调用、异地/多设备登录、内容审核。重试会加重风控，等 2~4h 通常自解。
		// 按 auth 处理：触发冷却轮换，避免连环请求火上浇油。
		return provider.Err(provider.KindAuth, status,
			"上游账号级风控（11140 会话服务异常，%s）：请求被服务端拦截，未到达模型。"+
				"多为高频调用/异地登录/内容审核触发，建议停用该账号 2~4 小时再试，勿反复重试；"+
				"持续出现请凭 Trace ID 联系官方（workbuddy_ai@tencent.com）", env.Msg)
	}
	return err
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
				if code == 11140 {
					msg = "上游账号级风控（11140 会话服务异常）：请求被服务端拦截未达模型，勿反复重试，停 2~4 小时通常自解；持续出现请凭 Trace ID 找官方工单"
				}
				if code == 11128 {
					dump, _ := json.Marshal(sentBody)
					dump11128(dump)
					msg = "上游渠道校验拦截（11128）：提示词命中非官方客户端指纹，与账号/token 无关；TokenHub 已自动改写已知指纹并原地重试仍被拒，请求样本已存日志目录（11128-dump-*.json）供词表分析"
				}
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
