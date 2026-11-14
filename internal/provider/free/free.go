// Package free 免费模型提供商：原生直连 Kilo AI 公共网关的免费模型池。
//
// Kilo（kilo.ai）的 OpenAI 兼容网关对 isFree 模型完全开放——免账号、免 Key、
// 免指纹门槛，标准 POST 即可调用。TokenHub 直接以标准 OpenAI 协议接入，
// 零外部依赖（协议参考 dsh-our-free-model 插件 kilo.js 的线上行为）：
//
//	GET  {base}/models            → {data:[{id,name,isFree,…}]}
//	POST {base}/chat/completions  → SSE 或单个 JSON（网关高负载时）
//
// 归因头（http-referer/x-title）与 Kilo 自家客户端一致，网关按 referer
// 区分流量来源。注意：免费池的 prompt 可能被上游记录用于改进服务，
// 仅适合日常轻量使用，不要发敏感内容。
package free

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const defaultBase = "https://ai-gateway.kilo.ai/api/v1"

const (
	listingTimeout = 15 * time.Second
	// 模型清单缓存：免费池成员变化很少（上游偶尔上下架），
	// 缓存 10 分钟，避免额度刷新周期与 /v1/models 请求反复打网关。
	listingTTL = 10 * time.Minute
)

// 归因头：Kilo 官方文档标注 optional，网关按 referer 排流量优先级。
var attribution = map[string]string{
	"http-referer": "https://kilo.ai/",
	"x-title":      "TokenHub",
}

// Provider 实现 provider.Provider。无需凭据：池子里挂一个伪账号即可。
type Provider struct {
	cfg *config.Config

	mu       sync.Mutex
	models   []provider.ModelInfo
	modelsAt time.Time // 零值=无缓存
}

func New(cfg *config.Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string { return store.PFree }

func (p *Provider) base() string {
	if pc := p.cfg.PC(store.PFree); pc != nil && pc.APIBase != "" {
		return strings.TrimRight(pc.APIBase, "/")
	}
	return defaultBase
}

func headers(stream bool) map[string]string {
	h := map[string]string{
		"accept":     "application/json",
		"user-agent": "TokenHub",
	}
	if stream {
		h["accept"] = "text/event-stream"
	}
	for k, v := range attribution {
		h[k] = v
	}
	return h
}

// ── 模型清单 ──

var vendorPrefix = regexp.MustCompile(`^[^:]{1,40}:\s+`)
var freeSuffix = regexp.MustCompile(`\s*\(free\)\s*$`)

// cleanName 清洗清单显示名：去掉 "Vendor: " 前缀与 " (free)" 后缀。
func cleanName(raw string) string {
	s := strings.TrimSpace(raw)
	s = vendorPrefix.ReplaceAllString(s, "")
	s = freeSuffix.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// fetchListing 拉一次模型清单（GET /models，标准 OpenAI 形状 + isFree 扩展字段）。
func (p *Provider) fetchListing(ctx context.Context) ([]provider.ModelInfo, error) {
	var out struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			IsFree       bool   `json:"isFree"`
			ContextLen   int64  `json:"context_length"`
			TopProvider  struct {
				MaxCompletionTokens int64 `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Architecture struct {
				Input []string `json:"input_modalities"`
			} `json:"architecture"`
			Params []string `json:"supported_parameters"`
		} `json:"data"`
	}
	url := p.base() + "/models"
	status, err := httpx.DoJSON(ctx, "GET", url, headers(false), nil, &out, listingTimeout)
	if err != nil {
		return nil, provider.Err(provider.KindNetwork, 0, "免费模型清单拉取失败: %v", err)
	}
	if status >= 400 {
		return nil, provider.ClassifyBody(status, nil)
	}
	seen := map[string]bool{}
	var list []provider.ModelInfo
	for _, m := range out.Data {
		// 只收 isFree 行：网关上其余都是付费 id，免费车道调它必 401。
		if !m.IsFree || m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		mi := provider.ModelInfo{ID: m.ID, Name: cleanName(m.Name), MaxInput: m.ContextLen, MaxOutput: m.TopProvider.MaxCompletionTokens}
		for _, p := range m.Params {
			if p == "reasoning" {
				mi.Reasoning = true
				break
			}
		}
		for _, mod := range m.Architecture.Input {
			if mod == "image" {
				mi.Vision = true
				break
			}
		}
		list = append(list, mi)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

// SeedModels 静态兜底：清单拉不到时不至于让分组消失（极端兜底，正常不依赖）。
func (p *Provider) SeedModels() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "kilo-auto/free", Name: "Auto Free"}}
}

// Models 免费池清单（带 10 分钟缓存）。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.models != nil && time.Since(p.modelsAt) < listingTTL {
		return p.models, nil
	}
	list, err := p.fetchListing(ctx)
	if err != nil {
		// 缓存过期但上游临时故障：回退旧清单（有的用总比空着强）
		if p.models != nil && time.Since(p.modelsAt) < 24*time.Hour {
			return p.models, nil
		}
		return nil, err
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "Kilo 免费池清单为空（可能上游临时维护，稍后重试）")
	}
	p.models = list
	p.modelsAt = time.Now()
	return p.models, nil
}

// HasModel 同步判断 id 是否属于免费池（只查缓存/按命名约定判断，不发网络请求）。
// 供账号池路由 Kilo 自带 ":" 的模型 id（如 nvidia/xxx:free）——这类 id 会撞
// "provider:model" 前缀解析，池子在逐段认领失败后调它兜底。
func (p *Provider) HasModel(id string) bool {
	if id == "" {
		return false
	}
	p.mu.Lock()
	hasCache := p.models != nil && time.Since(p.modelsAt) < 24*time.Hour
	if hasCache {
		for _, m := range p.models {
			if strings.EqualFold(m.ID, id) { // 大小写不敏感：客户端常发 GLM-5.3-Flash 这类变形
				p.mu.Unlock()
				return true
			}
		}
	}
	p.mu.Unlock()
	if hasCache {
		return false // 缓存有效且没有：明确不属于
	}
	// 无缓存时的命名约定兜底：Kilo 免费 id 以 ":free" 结尾（kilo-auto/free、
	// openrouter/free 是官方自动路由 id，同样属于免费池）。
	low := strings.ToLower(id)
	return strings.HasSuffix(low, ":free") || low == "kilo-auto/free" || low == "openrouter/free"
}

// ── 额度 / 签到 / 刷新 ──

// Quota 免费池无额度概念：报模型数与提示。
func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	list, err := p.Models(ctx, acct)
	if err != nil {
		return nil, err
	}
	n := float64(len(list))
	return &store.QuotaSnapshot{
		Total:     n,
		Remain:    n,
		Unit:      "个模型",
		Plan:      "Kilo 免费池",
		Source:    "external",
		Note:      "免费共用池：上游可能记录 prompt 用于改进服务，勿发敏感内容",
		UpdatedAt: time.Now().Unix(),
	}, nil
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return false }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}

// Refresh 免费池无凭据：校验清单连通性。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	p.mu.Lock() // Refresh 走一次强制拉取（绕缓存），顺便更新缓存
	defer p.mu.Unlock()
	list, err := p.fetchListing(ctx)
	if err != nil {
		return err
	}
	if len(list) > 0 {
		p.models = list
		p.modelsAt = time.Now()
	}
	return nil
}

// ── 对话 ──

// Chat 透传 OpenAI 格式请求（含 tools），强制流式；伪账号不参与鉴权。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	var body map[string]any
	if err := json.Unmarshal(openaiReq, &body); err != nil {
		sink.Close()
		return provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	body["stream"] = true
	pb, _ := json.Marshal(body)
	promptChars := estimatePrompt(body)
	resp, err := httpx.DoStream(ctx, "POST", p.base()+"/chat/completions", headers(true), pb, p.cfg.UpstreamTimeout())
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindNetwork, 0, "Kilo 网关连接失败: %v", err)
	}
	if resp.Status >= 400 {
		sink.Close()
		return classifyKilo(resp.Status, resp.Body)
	}
	// 网关高负载时会对 stream 请求返回单个 JSON 文档（content-type 仍是 json）：
	// 按响应体形状判定，JSON 且无 error 就折叠成一次性 delta 吐给 sink。
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		b, _ := io.ReadAll(io.LimitReader(resp.Stream, 8<<20))
		resp.Stream.Close()
		sink.Close()
		return finishJSON(b, sink, promptChars)
	}
	return parseSSE(resp.Stream, sink, promptChars)
}

// finishJSON 处理"流请求返回单个 JSON 文档"的形状（含错误信封）。
func finishJSON(b []byte, sink provider.Sink, promptChars int) error {
	var payload struct {
		Choices []struct {
			Message struct {
				Role             string `json:"role"`
				Content          any    `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason any `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
		Error   any    `json:"error"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &payload) != nil {
		return provider.Err(provider.KindServer, 200, "Kilo 返回了无法解析的响应，请重试")
	}
	if payload.Error != nil || (len(payload.Choices) == 0 && payload.Code != nil) {
		return classifyKilo(200, b)
	}
	if len(payload.Choices) == 0 {
		sink.StreamError(0, "Kilo 返回空响应，请重试")
		return nil
	}
	m := payload.Choices[0].Message
	hasDelta := false
	if m.Role != "" {
		sink.Delta(provider.Delta{Role: m.Role})
		hasDelta = true
	}
	if s, ok := m.Content.(string); ok && s != "" {
		sink.Delta(provider.Delta{Content: s})
		hasDelta = true
	}
	reasoning := m.Reasoning
	if reasoning == "" {
		reasoning = m.ReasoningContent
	}
	if reasoning != "" {
		sink.Delta(provider.Delta{Reasoning: reasoning})
		hasDelta = true
	}
	usage := usageFrom(payload.Usage)
	finishReason := ""
	if fr, ok := payload.Choices[0].FinishReason.(string); ok {
		finishReason = fr
	}
	return finishSink(sink, hasDelta, finishReason, usage, promptChars)
}

// parseSSE 解析 Kilo 的 OpenAI 风格 SSE。
// 兼容 reasoning / reasoning_content 思考字段；网关排程期间的 ": KILO PROCESSING"
// 注释行由 ScanSSE 自动跳过（同时保活空闲判定）。
func parseSSE(r io.Reader, sink provider.Sink, promptChars int) error {
	defer sink.Close()
	firstRoleSent := false
	hasDelta := false
	finishReason := ""
	var usage *provider.Usage

	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			return data != "[DONE]"
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Role             string `json:"role"`
					Content          any    `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
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
			Error any    `json:"error"`
			Code  any    `json:"code"`
			Msg   string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return true
		}
		if chunk.Error != nil || (len(chunk.Choices) == 0 && chunk.Code != nil) {
			code, msg := extractError(chunk.Code, chunk.Error, chunk.Msg)
			sink.StreamError(code, msg)
			return false
		}
		if len(chunk.Choices) == 0 {
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
		if delta.Reasoning = d.Reasoning; delta.Reasoning == "" {
			delta.Reasoning = d.ReasoningContent
		}
		for _, tc := range d.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			td := &provider.ToolDelta{Index: idx, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments}
			hasDelta = true
			sink.Delta(provider.Delta{Role: delta.Role, Tool: td})
			delta.Role = ""
		}
		if delta.Content != "" || delta.Reasoning != "" || delta.Role != "" {
			hasDelta = true
			sink.Delta(delta)
		}
		if fr, ok := c.FinishReason.(string); ok && fr != "" {
			finishReason = fr
		}
		if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
			usage = usageFrom(chunk.Usage)
		}
		return true
	})
	if err != nil {
		return err
	}
	return finishSink(sink, hasDelta, finishReason, usage, promptChars)
}

func usageFrom(u *struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}) *provider.Usage {
	if u == nil || (u.PromptTokens == 0 && u.CompletionTokens == 0) {
		return nil
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return &provider.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: total, Present: true}
}

func finishSink(sink provider.Sink, hasDelta bool, finishReason string, usage *provider.Usage, promptChars int) error {
	if !hasDelta && usage == nil {
		sink.StreamError(0, "Kilo 免费池返回空流，请重试")
		return nil
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if usage == nil {
		prompt := int64(float64(promptChars) * 0.6)
		if prompt < 1 {
			prompt = 1
		}
		usage = &provider.Usage{PromptTokens: prompt, CompletionTokens: 1, TotalTokens: prompt + 1, Present: true}
	}
	sink.Finish(provider.Finish{Reason: finishReason, Usage: usage})
	return nil
}

func estimatePrompt(body map[string]any) int {
	msgs, _ := body["messages"].([]any)
	n := 0
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			n += len([]rune(s))
		}
	}
	return n
}

// classifyKilo Kilo 错误信封分类。付费模型（免费车道不提供）报成人话的模型错误，
// 审核拦截归为 client（上游的答案，不是传输故障），其余交给通用分类。
func classifyKilo(status int, body []byte) error {
	text := string(body)
	lower := strings.ToLower(text)
	if strings.Contains(lower, "paid_model_auth_required") {
		return provider.Err(provider.KindModel, status, "该模型不在 Kilo 免费池（付费模型需要 Kilo 账号），请换用免费池模型")
	}
	if strings.Contains(lower, "moderation") || strings.Contains(lower, "flagged") || strings.Contains(lower, "filtered") {
		return provider.Err(provider.KindClient, status, "%s", text)
	}
	if strings.Contains(text, "\"error\"") || strings.Contains(text, "error_type") {
		return provider.ClassifyBody(status, body)
	}
	return provider.ClassifyBody(status, body)
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
		if message == "" {
			message = e
		}
	case map[string]any:
		if m, ok := e["message"].(string); ok && message == "" {
			message = m
		}
	}
	if message == "" {
		message = "Kilo 免费池上游错误"
	}
	return codeN, message
}
