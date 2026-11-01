// Package custom 外部自定义 API 提供商。
// 用户在面板填入任意第三方 API 地址 + Key（OpenAI 兼容或 Anthropic 兼容），
// TokenHub 把它当成一个普通账号挂进池子：轮换、冷却、用量统计、模型前缀路由全部复用。
// 一个账号 = 一个外部 API 端点；多家外部 API 各建一个账号、各用各的 slug 前缀。
package custom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const anthropicVersion = "2023-06-01"

// Provider 实现 provider.Provider。凭据在账号里（AccessToken = API Key）。
type Provider struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string { return store.PCustom }

// ── 账号字段解析 ──

// BaseURL 归一化上游地址：补 https:// 前缀、去尾部斜杠。
func BaseURL(acct *store.Account) string {
	b := strings.TrimSpace(acct.ExtraGet(store.ExtraCustomBaseURL))
	if b == "" {
		return ""
	}
	if !strings.Contains(b, "://") {
		b = "https://" + b
	}
	return strings.TrimRight(b, "/")
}

// Style 接口类型：openai（默认）| anthropic。
func Style(acct *store.Account) string {
	if strings.EqualFold(acct.ExtraGet(store.ExtraCustomStyle), "anthropic") {
		return "anthropic"
	}
	return "openai"
}

// DeclaredModels 账号声明的模型列表（逗号/空格/换行分隔）。
func DeclaredModels(acct *store.Account) []string {
	raw := strings.ReplaceAll(acct.ExtraGet(store.ExtraCustomModels), "\n", ",")
	var out []string
	for _, m := range strings.Split(raw, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

// Slug 模型前缀别名（小写）。
func Slug(acct *store.Account) string {
	return strings.ToLower(strings.TrimSpace(acct.ExtraGet(store.ExtraCustomSlug)))
}

// endpoints 按地址形态拼接端点：
//   - 地址已以版本段结尾（/v1、/v2、/v4、/api/paas/v4、/v1beta 等）→ 直接追加动作
//     （用户填 https://open.bigmodel.cn/api/paas/v4 时得到 …/v4/chat/completions，
//     而不是错误的 …/v4/v1/chat/completions）
//   - 其余（裸域名，如 https://api.deepseek.com）→ 补 /v1 再追加动作
func endpoints(base, style string) (chatURL, modelsURL string) {
	base = strings.TrimRight(base, "/")
	if hasVersionSegment(base) {
		if style == "anthropic" {
			return base + "/messages", base + "/models"
		}
		return base + "/chat/completions", base + "/models"
	}
	if style == "anthropic" {
		return base + "/v1/messages", base + "/v1/models"
	}
	return base + "/v1/chat/completions", base + "/v1/models"
}

// hasVersionSegment 判断地址最后一段是否为 API 版本段（v + 数字开头，如 v1/v4/v1beta）。
func hasVersionSegment(base string) bool {
	segs := strings.Split(base, "/")
	last := segs[len(segs)-1]
	if len(last) < 2 || last[0] != 'v' && last[0] != 'V' {
		return false
	}
	return last[1] >= '1' && last[1] <= '9'
}

func headers(acct *store.Account, style string) map[string]string {
	key := acct.AccessToken
	if style == "anthropic" {
		return map[string]string{
			"content-type":      "application/json",
			"x-api-key":         key,
			"anthropic-version": anthropicVersion,
		}
	}
	return map[string]string{
		"content-type":  "application/json",
		"authorization": "Bearer " + key,
	}
}

// ── Provider 接口 ──

func (p *Provider) SeedModels() []provider.ModelInfo { return nil } // 模型由账号声明或动态发现

// Models 账号声明的模型列表优先；未声明时尝试上游 /v1/models 自动发现。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	if list := DeclaredModels(acct); len(list) > 0 {
		return toModelInfos(list), nil
	}
	list, err := DiscoverModels(ctx, BaseURL(acct), acct.AccessToken, Style(acct))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "外部 API 未声明模型且自动发现为空：请在账号里填模型列表")
	}
	return toModelInfos(list), nil
}

func toModelInfos(ids []string) []provider.ModelInfo {
	out := make([]provider.ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, provider.ModelInfo{ID: id})
	}
	return out
}

// DiscoverModels 调上游 GET /v1/models 拉模型列表（OpenAI / Anthropic 兼容端点同构）。
func DiscoverModels(ctx context.Context, base, key, style string) ([]string, error) {
	if base == "" {
		return nil, provider.Err(provider.KindClient, 0, "接口地址为空")
	}
	_, modelsURL := endpoints(base, style)
	h := headers(&store.Account{AccessToken: key}, style)
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	status, err := httpx.DoJSON(ctx, "GET", modelsURL, h, nil, &out, 20*time.Second)
	if err != nil {
		return nil, provider.Err(provider.KindNetwork, 0, "模型列表拉取失败: %v", err)
	}
	if status >= 400 {
		return nil, provider.ClassifyBody(status, nil)
	}
	seen := map[string]bool{}
	var list []string
	for _, m := range out.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			list = append(list, m.ID)
		}
	}
	sort.Strings(list)
	return list, nil
}

// Quota 外部 API 无统一额度接口：报连通性与模型数。
func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	base := BaseURL(acct)
	if base == "" {
		return nil, provider.Err(provider.KindClient, 0, "接口地址为空")
	}
	style := Style(acct)
	plan := "外部 API（" + style + "）"
	n := len(DeclaredModels(acct))
	if n == 0 {
		if list, err := DiscoverModels(ctx, base, acct.AccessToken, style); err == nil {
			n = len(list)
		}
	}
	if n > 0 {
		plan += fmt.Sprintf(" · %d 个模型", n)
	}
	return &store.QuotaSnapshot{
		Source:    "external",
		Plan:      plan,
		Note:      "按上游计费，TokenHub 不统计余额",
		UpdatedAt: time.Now().Unix(),
	}, nil
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return false }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}

// Refresh 外部 API 的 key 无刷新机制：校验连通性（失败抛 auth，让面板看到 key 失效）。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	_, err := DiscoverModels(ctx, BaseURL(acct), acct.AccessToken, Style(acct))
	return err
}

// ── 对话：OpenAI 兼容上游 ──

// Chat 按账号的接口类型分流。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	base := BaseURL(acct)
	if base == "" {
		sink.Close()
		return provider.Err(provider.KindClient, 0, "外部 API 账号缺少接口地址，请在面板编辑账号")
	}
	if Style(acct) == "anthropic" {
		return p.chatAnthropic(ctx, acct, base, openaiReq, sink)
	}
	return p.chatOpenAI(ctx, acct, base, openaiReq, sink)
}

func (p *Provider) chatOpenAI(ctx context.Context, acct *store.Account, base string, openaiReq []byte, sink provider.Sink) error {
	chatURL, _ := endpoints(base, "openai")
	var body map[string]any
	if err := json.Unmarshal(openaiReq, &body); err != nil {
		sink.Close()
		return provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	body["stream"] = true
	pb, _ := json.Marshal(body)
	promptChars := estimatePrompt(body)
	resp, err := httpx.DoStream(ctx, "POST", chatURL, headers(acct, "openai"), pb, p.cfg.UpstreamTimeout())
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindNetwork, 0, "外部 API 连接失败: %v", err)
	}
	if resp.Status >= 400 {
		sink.Close()
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		// 2xx + JSON：业务错误信封（非 SSE）
		b, _ := io.ReadAll(io.LimitReader(resp.Stream, 1<<20))
		resp.Stream.Close()
		sink.Close()
		if len(strings.TrimSpace(string(b))) == 0 {
			return provider.Err(provider.KindServer, 200, "外部 API 返回空响应，请重试")
		}
		return provider.ClassifyBody(resp.Status, b)
	}
	return parseOpenAISSE(resp.Stream, sink, promptChars)
}

// parseOpenAISSE 解析 OpenAI 风格 SSE（兼容 reasoning / reasoning_content 思考字段）。
func parseOpenAISSE(r io.Reader, sink provider.Sink, promptChars int) error {
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
			u := chunk.Usage
			total := u.TotalTokens
			if total == 0 {
				total = u.PromptTokens + u.CompletionTokens
			}
			usage = &provider.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: total, Present: true}
		}
		return true
	})
	if err != nil {
		return err
	}
	return finishSink(sink, hasDelta, finishReason, usage, promptChars, "外部 API")
}

func finishSink(sink provider.Sink, hasDelta bool, finishReason string, usage *provider.Usage, promptChars int, label string) error {
	if !hasDelta && usage == nil {
		sink.StreamError(0, label+" 返回空流，请重试")
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
		message = "外部 API 上游错误"
	}
	return codeN, message
}
