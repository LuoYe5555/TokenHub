// Package qwen 千问网页版（chat.qwen.ai）上游客户端。
// 协议移植自开源实现 qwen3-reverse / qwengate / Qwen2OpenAI-Combatible：
// Bearer Token 鉴权（网页会话令牌），/api/v2/chat/completions SSE，
// 增量帧带 phase（think / thinking_summary / answer）与 status（finished）。
// 网页版没有公开的额度/签到接口，Quota 仅校验令牌并返回用户信息。
package qwen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const (
	defaultBase = "https://chat.qwen.ai"
	bxV         = "2.5.36"
	timezone    = "Mon Sep 29 2025 09:52:39 GMT+0800"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

// Provider 实现 provider.Provider。
type Provider struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Provider {
	httpx.RegisterWarm(baseOf(cfg))
	return &Provider{cfg: cfg}
}

func baseOf(cfg *config.Config) string {
	if pc := cfg.PC(store.PQwen); pc != nil && pc.APIBase != "" {
		return pc.APIBase
	}
	return defaultBase
}

func (p *Provider) base() string { return baseOf(p.cfg) }

func (p *Provider) Name() string { return store.PQwen }

// headers 浏览器形状请求头（WAF 会校验 sec-ch-ua / bx-* 等客户端提示）。
// bx-umidtoken 按账号令牌稳定派生，避免每次请求变化触发风控。
func headers(acct *store.Account, base, chatID string) map[string]string {
	sum := sha256.Sum256([]byte("qwen-umid:" + acct.AccessToken))
	umid := hex.EncodeToString(sum[:])[:64]
	referer := base + "/"
	if chatID != "" {
		referer = base + "/c/" + chatID
	}
	return map[string]string{
		"accept":             "application/json, text/plain, */*",
		"accept-language":    "zh-CN,zh;q=0.9",
		"content-type":       "application/json",
		"source":             "web",
		"user-agent":         userAgent,
		"origin":             base,
		"referer":            referer,
		"sec-ch-ua":          `"Chromium";v="145", "Google Chrome";v="145", "Not?A_Brand";v="99"`,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-origin",
		"bx-v":               bxV,
		"bx-umidtoken":       umid,
		"bx-ua":              userAgent,
		"timezone":           timezone,
		"version":            "0.2.7",
		"x-request-id":       provider.NewUUID(),
		"x-accel-buffering":  "no",
		"authorization":      "Bearer " + acct.AccessToken,
	}
}

// fetchUser 拉取账号信息（兼做令牌有效性校验）。
func (p *Provider) fetchUser(ctx context.Context, acct *store.Account) (map[string]any, error) {
	var out map[string]any
	status, err := httpx.DoJSON(ctx, "GET", p.base()+"/api/v1/auths/", headers(acct, p.base(), ""), nil, &out, 30*time.Second)
	if err != nil {
		if status == 401 || status == 403 {
			return nil, provider.Err(provider.KindAuth, status, "千问令牌无效或已过期")
		}
		// 令牌失效时上游可能回 HTML 登录页，DoJSON 会报解码错误——按鉴权失败处理
		if status == 200 {
			return nil, provider.Err(provider.KindAuth, 200, "千问返回非 JSON（令牌可能已失效）")
		}
		return nil, provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	if status >= 400 {
		return nil, provider.ClassifyBody(status, nil)
	}
	return out, nil
}

// Quota 千问网页版无公开额度接口：校验令牌并返回用户信息型快照。
func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	user, err := p.fetchUser(ctx, acct)
	if err != nil {
		return nil, err
	}
	name, _ := user["name"].(string)
	email, _ := user["email"].(string)
	if name == "" {
		name = email
	}
	if acct.Nickname == "" && name != "" {
		acct.Nickname = name
	}
	return &store.QuotaSnapshot{
		Source:    "userinfo",
		Plan:      "Qwen Web",
		Note:      "千问网页版无额度接口，令牌有效",
		UpdatedAt: time.Now().Unix(),
	}, nil
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return false }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}

// Refresh 网页令牌无刷新机制：仅校验有效性（失效时返回 KindAuth 让池策略处理）。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	_, err := p.fetchUser(ctx, acct)
	return err
}

// ── 模型 ──

// seedModels 上游目录不可用时的兜底模型表（2026 站点在售文本模型）。
var seedModels = []provider.ModelInfo{
	{ID: "qwen3.7-max"}, {ID: "qwen3.6-plus"}, {ID: "qwen3.6-max-preview"},
	{ID: "qwen3.5-plus"}, {ID: "qwen3.5-max-2026-03-08"}, {ID: "qwen3.5-flash"},
	{ID: "qwen3-coder-plus"}, {ID: "qwen3-vl-plus"},
}

func (p *Provider) SeedModels() []provider.ModelInfo { return seedModels }

// Models 动态模型目录（/api/models，5 分钟缓存）。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	if cached, ok := modelsCache(); ok {
		return cached, nil
	}
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"data"`
	}
	status, err := httpx.DoJSON(ctx, "GET", p.base()+"/api/models", headers(acct, p.base(), ""), nil, &out, 30*time.Second)
	if err != nil {
		if status == 401 || status == 403 {
			return nil, provider.Err(provider.KindAuth, status, "千问令牌无效或已过期")
		}
		return nil, provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	if len(out.Data) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "模型目录为空")
	}
	var list []provider.ModelInfo
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		list = append(list, provider.ModelInfo{ID: m.ID, Name: m.Name})
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "模型目录为空")
	}
	setModelsCache(list)
	return list, nil
}

type cacheEntry struct {
	at     time.Time
	models []provider.ModelInfo
}

var (
	mcacheMu sync.Mutex
	mcache   *cacheEntry
)

func modelsCache() ([]provider.ModelInfo, bool) {
	mcacheMu.Lock()
	defer mcacheMu.Unlock()
	if mcache != nil && time.Since(mcache.at) < 5*time.Minute {
		return mcache.models, true
	}
	return nil, false
}

func setModelsCache(models []provider.ModelInfo) {
	mcacheMu.Lock()
	defer mcacheMu.Unlock()
	mcache = &cacheEntry{at: time.Now(), models: models}
}

// ── 对话 ──

// parsedReq 解析后的对话请求。
type parsedReq struct {
	model    string // 去掉功能后缀的基础模型
	content  string // 折叠后的完整 user content
	thinking bool   // 是否启用思考
}

// parseReq 把 OpenAI 请求折叠为千问网页版的单条 user 消息
// （多轮用 <｜User｜>/<｜Assistant｜> 特殊标记折叠，移植自 Qwen2OpenAI 实测协议）。
func parseReq(openaiReq []byte, defaultModel string) (*parsedReq, error) {
	var src struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(openaiReq, &src); err != nil {
		return nil, provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	model := strings.TrimSpace(src.Model)
	if model == "" {
		model = defaultModel
	}
	thinking := false
	base := model
	// 功能后缀 → 行为开关（与 Qwen2OpenAI / qwengate 一致）
	switch {
	case strings.HasSuffix(base, "-thinking"):
		base = strings.TrimSuffix(base, "-thinking")
		thinking = true
	case strings.HasSuffix(base, "-no-thinking"):
		base = strings.TrimSuffix(base, "-no-thinking")
	}
	extract := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		// 多模态数组：取全部 text 部分
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			var sb strings.Builder
			for _, pt := range parts {
				if pt.Type == "text" && pt.Text != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(pt.Text)
				}
			}
			return sb.String()
		}
		return ""
	}
	var system, userCur string
	var conv []string
	flushUser := func() {
		if userCur != "" {
			conv = append(conv, userCur)
			userCur = ""
		}
	}
	for _, m := range src.Messages {
		text := extract(m.Content)
		switch m.Role {
		case "system", "developer":
			if system != "" {
				system += "\n\n"
			}
			system += text
		case "user":
			flushUser()
			userCur = text
		case "assistant":
			flushUser()
			conv = append(conv, fmt.Sprintf("<｜Assistant｜>%s<｜end of sentence｜>", text))
		}
	}
	flushUser()
	content := strings.Join(conv, "<｜User｜>")
	if system != "" {
		if content != "" {
			content = system + "\n\n" + content
		} else {
			content = system
		}
	}
	if content == "" {
		return nil, provider.Err(provider.KindClient, 400, "messages 为空")
	}
	return &parsedReq{model: base, content: content, thinking: thinking}, nil
}

// featureConfig 千问网页版的思考/输出控制（output_schema=phase 分帧）。
func featureConfig(thinking bool) map[string]any {
	return map[string]any{
		"thinking_enabled": true,
		"output_schema":    "phase",
		"auto_thinking":    thinking,
		"thinking_mode":    "Auto",
		"thinking_format":  "summary",
		"auto_search":      false,
		"research_mode":    "normal",
	}
}

// Chat 实现 provider.Provider。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	base := p.base()
	req, err := parseReq(openaiReq, p.cfg.PC(store.PQwen).DefaultModel)
	if err != nil {
		sink.Close()
		return err
	}
	// 1) 新建会话
	h := headers(acct, base, "")
	newChat := map[string]any{
		"title":      "API Chat",
		"models":     []string{req.model},
		"chat_mode":  "normal",
		"chat_type":  "t2t",
		"timestamp":  time.Now().UnixMilli(),
		"urf":        "",
		"project_id": "",
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	nb, _ := json.Marshal(newChat)
	status, err := httpx.DoJSON(ctx, "POST", base+"/api/v2/chats/new", h, nb, &created, 30*time.Second)
	if err != nil {
		sink.Close()
		if status == 401 || status == 403 {
			return provider.Err(provider.KindAuth, status, "千问令牌无效或已过期")
		}
		return provider.Err(provider.KindNetwork, 0, "新建会话失败: %v", err)
	}
	if status >= 400 {
		sink.Close()
		return provider.ClassifyBody(status, nil)
	}
	chatID := created.Data.ID
	if chatID == "" {
		sink.Close()
		return provider.Err(provider.KindServer, 0, "新建会话失败：上游未返回 chat_id")
	}
	// 2) 发送对话（SSE）
	h = headers(acct, base, chatID)
	fid := provider.NewUUID()
	childID := provider.NewUUID()
	ts := time.Now().Unix()
	payload := map[string]any{
		"stream":             true,
		"version":            "2.1",
		"incremental_output": true,
		"chat_id":            chatID,
		"chat_mode":          "normal",
		"model":              req.model,
		"parent_id":          nil,
		"messages": []map[string]any{{
			"fid":            fid,
			"parentId":       nil,
			"childrenIds":    []string{childID},
			"role":           "user",
			"content":        req.content,
			"user_action":    "chat",
			"files":          []any{},
			"timestamp":      ts,
			"models":         []string{req.model},
			"chat_type":      "t2t",
			"feature_config": featureConfig(req.thinking),
			"extra":          map[string]any{"meta": map[string]any{"subChatType": "t2t"}},
			"sub_chat_type":  "t2t",
			"parent_id":      nil,
		}},
		"timestamp": ts + 1,
	}
	pb, _ := json.Marshal(payload)
	resp, err := httpx.DoStream(ctx, "POST", base+"/api/v2/chat/completions?chat_id="+chatID, h, pb, p.cfg.ConnectTimeout())
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
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		sink.Close()
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	return parseSSE(resp.Stream, sink)
}

// parseSSE 解析千问网页版 SSE（phase 分帧）。
func parseSSE(r io.Reader, sink provider.Sink) error {
	defer sink.Close()
	firstRoleSent := false
	hasDelta := false
	started := false
	finishReason := ""

	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			return true
		}
		var frame struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
			Error   any    `json:"error"`
			// "response.created"（点号键名，上游原样）
			ResponseCreated *struct {
				ChatID     string `json:"chat_id"`
				ResponseID string `json:"response_id"`
			} `json:"response.created"`
			Choices []struct {
				Delta struct {
					Phase   string `json:"phase"`
					Status  string `json:"status"`
					Content any    `json:"content"`
				} `json:"delta"`
				FinishReason any `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &frame) != nil {
			return true
		}
		if frame.ResponseCreated != nil {
			started = true
			return true
		}
		// 错误帧：显式 error / type=error / 裸 code+message
		if frame.Error != nil || frame.Type == "error" || (len(frame.Choices) == 0 && frame.Code != nil) {
			code, msg := extractError(frame.Code, frame.Error, frame.Message, frame.Type)
			sink.StreamError(code, msg)
			return false
		}
		if len(frame.Choices) == 0 {
			return true
		}
		d := frame.Choices[0].Delta
		content, _ := d.Content.(string)
		switch d.Phase {
		case "think", "thinking_summary":
			if content != "" && d.Status != "finished" {
				delta := provider.Delta{Reasoning: content}
				if !firstRoleSent {
					delta.Role = "assistant"
					firstRoleSent = true
				}
				hasDelta = true
				sink.Delta(delta)
			}
		case "answer", "":
			if content != "" && d.Status != "finished" {
				delta := provider.Delta{Content: content}
				if !firstRoleSent {
					delta.Role = "assistant"
					firstRoleSent = true
				}
				hasDelta = true
				sink.Delta(delta)
			}
			if d.Status == "finished" {
				finishReason = "stop"
			}
		}
		if fr, ok := frame.Choices[0].FinishReason.(string); ok && fr != "" {
			finishReason = fr
		}
		return true
	})
	if err != nil {
		return err
	}
	if !started && !hasDelta {
		sink.StreamError(0, "千问上游返回空流（令牌可能已失效或触发风控）")
		return nil
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	sink.Finish(provider.Finish{Reason: finishReason})
	return nil
}

func extractError(code any, errObj any, message, typ string) (int64, string) {
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
		if c, ok := e["code"].(float64); ok && codeN == 0 {
			codeN = int64(c)
		}
	}
	if message == "" {
		message = "千问上游错误"
	}
	if typ != "" {
		message = "[" + typ + "] " + message
	}
	return codeN, message
}
