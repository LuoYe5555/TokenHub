// Package opencode 免费模型提供商：原生直连 opencode.ai 免费推理车道。
//
// opencode.ai 的免费档没有公开 API，只有官方 CLI（Bun 运行时）可以调用：
// 上游按客户端指纹放行——铸造的会话 ID、官方 13 工具集、官方 CLI 系统提示、
// 以及 TLS 层的 Bun/BoringSSL ClientHello。本实现把整形与传输完整复刻
// （vendor 自 MIT 的 opencode2api，internal/oc/*），TokenHub 进程内直接
// 说出和官方客户端一样的"话"，免账号免 Key：
//
//	GET  {base}/openai/v1/models            → 标准清单（cost 0/0 即免费档）
//	POST {base}/openai/v1/chat/completions  → SSE（伪装官方 2.x CLI）
//
// 请求一旦 429（配额按"项目ID×出口IP"记账，报文区分不了指纹与配额），
// 换一套全新身份（会话+项目）重试。注意：免费池的 prompt 可能被上游记录
// 用于改进服务，仅适合日常轻量使用，不要发敏感内容。
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	ocgateway "tokenhub/internal/oc/gateway"
	ochttpx "tokenhub/internal/oc/httpx"
	ocidentity "tokenhub/internal/oc/identity"
	ocwire "tokenhub/internal/oc/wire"
	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const defaultBase = "https://opencode.ai/inference"

const (
	listingTimeout = 15 * time.Second
	listingTTL     = 10 * time.Minute
	chatAttempts   = 3 // 429 换新身份重试的上限
)

// Provider 实现 provider.Provider。无需凭据：池子里挂一个伪账号即可。
type Provider struct {
	cfg *config.Config

	mu       sync.Mutex
	models   []provider.ModelInfo
	modelsAt time.Time
}

func New(cfg *config.Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string { return store.POpencode }

func (p *Provider) base() string {
	if pc := p.cfg.PC(store.POpencode); pc != nil && pc.APIBase != "" {
		return strings.TrimRight(pc.APIBase, "/")
	}
	return defaultBase
}

// ── 模型清单 ──

// listingHeaders 清单走公开端点，普通客户端即可（无需指纹）。
func listingHeaders() map[string]string {
	return map[string]string{"accept": "application/json", "user-agent": "TokenHub", "authorization": "Bearer public"}
}

// fetchListing 拉一次清单，只收免费档：id 以 "-free" 结尾，或 cost 全 0
// （big-pickle 这类老牌免费 id 不带后缀，靠 cost 判定）。
func (p *Provider) fetchListing(ctx context.Context) ([]provider.ModelInfo, error) {
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Cost struct {
				Text struct {
					Input struct {
						Price float64 `json:"price"`
					} `json:"input"`
					Output struct {
						Price float64 `json:"price"`
					} `json:"output"`
				} `json:"text"`
			} `json:"cost"`
			Modalities struct {
				Input []string `json:"input"`
			} `json:"modalities"`
			Capabilities struct {
				Reasoning struct {
					Supported bool `json:"supported"`
				} `json:"reasoning"`
				Tools struct {
					Supported bool `json:"supported"`
				} `json:"tools"`
			} `json:"capabilities"`
			Limit struct {
				Context int64 `json:"context"`
				Input   int64 `json:"input"`
				Output  int64 `json:"output"`
			} `json:"limit"`
		} `json:"data"`
	}
	status, err := httpx.DoJSON(ctx, "GET", p.base()+"/openai/v1/models", listingHeaders(), nil, &out, listingTimeout)
	if err != nil {
		return nil, provider.Err(provider.KindNetwork, 0, "OpenCode 清单拉取失败: %v", err)
	}
	if status >= 400 {
		return nil, provider.ClassifyBody(status, nil)
	}
	seen := map[string]bool{}
	var list []provider.ModelInfo
	for _, m := range out.Data {
		free := strings.HasSuffix(m.ID, "-free") || (m.Cost.Text.Input.Price == 0 && m.Cost.Text.Output.Price == 0)
		if !free || m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		mi := provider.ModelInfo{ID: m.ID, Name: cleanName(m.Name), MaxInput: m.Limit.Context, MaxOutput: m.Limit.Output}
		mi.Reasoning = m.Capabilities.Reasoning.Supported
		mi.Tools = m.Capabilities.Tools.Supported
		for _, mod := range m.Modalities.Input {
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

var nameFix = strings.NewReplacer("-free", " Free", "big-pickle", "Big Pickle")

// cleanName 清洗清单显示名："-free" 后缀转成 " Free" 可读形式。
func cleanName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	return nameFix.Replace(s)
}

// SeedModels 静态兜底：清单拉不到时不至于让分组消失。
func (p *Provider) SeedModels() []provider.ModelInfo {
	return []provider.ModelInfo{
		{ID: "big-pickle", Name: "Big Pickle"},
		{ID: "glm-5.3-flash-free", Name: "GLM 5.3 Flash Free"},
		{ID: "deepseek-v4.1-flash-free", Name: "DeepSeek V4.1 Flash Free"},
	}
}

// Models 免费档清单（带 10 分钟缓存）。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.models != nil && time.Since(p.modelsAt) < listingTTL {
		return p.models, nil
	}
	list, err := p.fetchListing(ctx)
	if err != nil {
		if p.models != nil && time.Since(p.modelsAt) < 24*time.Hour {
			return p.models, nil
		}
		return nil, err
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "OpenCode 免费档清单为空（可能上游临时维护，稍后重试）")
	}
	p.models = list
	p.modelsAt = time.Now()
	return p.models, nil
}

// HasModel 同步判断 id 是否属于免费档（只查缓存/按命名约定判断，不发网络请求）。
func (p *Provider) HasModel(id string) bool {
	if id == "" {
		return false
	}
	p.mu.Lock()
	hasCache := p.models != nil && time.Since(p.modelsAt) < 24*time.Hour
	if hasCache {
		for _, m := range p.models {
			if strings.EqualFold(m.ID, id) { // 大小写不敏感
				p.mu.Unlock()
				return true
			}
		}
	}
	p.mu.Unlock()
	if hasCache {
		return false
	}
	// 无缓存时的命名约定兜底。
	low := strings.ToLower(id)
	return strings.HasSuffix(low, "-free") || low == "big-pickle"
}

// ── 额度 / 签到 / 刷新 ──

// Quota 免费档无额度概念：报模型数与提示。
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
		Plan:      "OpenCode 免费档",
		Source:    "external",
		Note:      "免费共用池：上游可能记录 prompt 用于改进服务，勿发敏感内容",
		UpdatedAt: time.Now().Unix(),
	}, nil
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return false }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}

// Refresh 校验清单连通性（绕缓存强制拉取）。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	p.mu.Lock()
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

// proxyFromEnv 本机代理（HTTPS_PROXY/https_proxy），与 PoC 验证行为一致。
func proxyFromEnv() *url.URL {
	raw := os.Getenv("HTTPS_PROXY")
	if raw == "" {
		raw = os.Getenv("https_proxy")
	}
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u
	}
	return nil
}

// Chat 伪装官方 CLI 调免费档（含 tools 透传），强制流式；伪账号不参与鉴权。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	defer sink.Close()
	var body map[string]any
	if err := json.Unmarshal(openaiReq, &body); err != nil {
		return provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	body["stream"] = true
	if _, ok := body["stream_options"]; !ok {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if _, has := body["max_completion_tokens"]; !has {
		if _, has2 := body["max_tokens"]; !has2 {
			body["max_completion_tokens"] = 32000
		}
	}
	// 整形：调用方工具改名为 clint_<digest> 并补全 13 个官方工具；注入官方 CLI 系统提示。
	renames, _ := ocgateway.RenameAndCompleteTools(body, ocwire.Chat, false)
	ocgateway.InjectOfficialPrompt(body, ocwire.Chat, false)
	payload, err := json.Marshal(body)
	if err != nil {
		return provider.Err(provider.KindClient, 400, "请求体序列化失败: %v", err)
	}
	promptChars := estimatePrompt(body)

	endpoint := p.base() + "/openai" + ocwire.Path(ocwire.Chat)
	proxy := proxyFromEnv()

	var lastStatus int
	var lastBody []byte
	for attempt := 0; attempt < chatAttempts; attempt++ {
		transport, err := ochttpx.NewBunTransport(ochttpx.BunOptions{
			Proxy:                 proxy,
			ConnectTimeout:        15 * time.Second,
			ResponseHeaderTimeout: p.cfg.UpstreamTimeout(),
		})
		if err != nil {
			return provider.Err(provider.KindServer, 0, "OpenCode 传输初始化失败: %v", err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
		if err != nil {
			return provider.Err(provider.KindClient, 400, "%v", err)
		}
		session := ocidentity.CanonicalSessionID("tokenhub-" + ocidentity.RandomID("s", 6))
		h := req.Header
		h.Set("content-type", "application/json")
		h.Set("authorization", "Bearer public")
		h.Set("x-opencode-client", "cli")
		h.Set("x-session-affinity", session)
		h.Set("x-session-id", session)
		h.Set("x-opencode-session", session)
		h.Set("x-opencode-session-id", session)
		h.Set("user-agent", ochttpx.V2UserAgent())
		h.Set("x-opencode-project", ocidentity.RandomProjectID())

		resp, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return provider.Err(provider.KindNetwork, 0, "OpenCode 网关连接失败: %v", err)
		}
		if resp.StatusCode < 400 {
			return p.consume(resp.Body, sink, promptChars, renames)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		transport.CloseIdleConnections()
		lastStatus, lastBody = resp.StatusCode, b
		// 429（配额/指纹合并报文）与 403：换全新身份重试——新项目 ID 就是新配额桶。
		if resp.StatusCode == 429 || resp.StatusCode == 403 {
			if attempt < chatAttempts-1 && ctx.Err() == nil {
				continue
			}
		}
		break
	}
	return classifyOC(lastStatus, lastBody)
}

// consume 解析成功响应的 SSE 并喂给 sink（含思考字段与工具调用还原）。
func (p *Provider) consume(r io.ReadCloser, sink provider.Sink, promptChars int, renames *ocwire.ToolRenames) error {
	defer r.Close()
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
			Error   any    `json:"error"`
			Code    any    `json:"code"`
			Msg     string `json:"message"`
			ErrType string `json:"type"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return true
		}
		if chunk.Error != nil || chunk.ErrType != "" || (len(chunk.Choices) == 0 && chunk.Code != nil) {
			code, msg := extractError(chunk.Code, chunk.Error, chunk.Msg)
			if msg == "" {
				msg = "OpenCode 免费档上游错误"
			}
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
			name := tc.Function.Name
			if renames != nil {
				if fixed, ok := renames.Resolve(name); ok { // clint_<digest> / 官方别名 → 调用方原名
					name = fixed
				}
			}
			td := &provider.ToolDelta{Index: idx, ID: tc.ID, Name: name, Args: tc.Function.Arguments}
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
		sink.StreamError(0, "OpenCode 免费档返回空流，请重试")
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

// classifyOC 错误信封分类。
func classifyOC(status int, body []byte) error {
	text := string(body)
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "freeusagelimit"):
		return provider.Err(provider.KindQuota, status, "OpenCode 免费档配额已用尽（按出口 IP 记账，约 12 小时后恢复；也可稍后重试换新配额桶）")
	case strings.Contains(lower, "freetiererror"):
		return provider.Err(provider.KindServer, status, "OpenCode 免费档拒绝了本次请求（客户端指纹门变动，需要等待 TokenHub 更新）")
	case strings.Contains(lower, "regionerror"):
		return provider.Err(provider.KindServer, status, "OpenCode 免费档在当前地区不可用，请配置代理后重试")
	case status == 401 || status == 403:
		return provider.Err(provider.KindServer, status, "OpenCode 免费档拒绝访问（%d）：%s", status, text)
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
	return codeN, message
}
