// Package qwen 千问网页版（chat.qwen.ai）上游客户端。
// 协议移植自开源实现 qwen3-reverse / qwengate / Qwen2OpenAI-Combatible：
// Bearer Token 鉴权（网页会话令牌），/api/v2/chat/completions SSE，
// 增量帧带 phase（think / thinking_summary / answer）与 status（finished）。
// 网页版没有公开的额度/签到接口，Quota 仅校验令牌并返回用户信息。
package qwen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
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
	version     = "0.3.19" // 站点前端版本头（CDP 抓包确认，过旧会被风控盯上）
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
// 风控签名优先级：账号里抓的真实浏览器 bx-ua/bx-umidtoken（CDP 嗅探，见
// web/qwenbrowser.go / profile.go）→ 按令牌派生的模拟值。
// 账号存有真实浏览器 cookie（一键导入时抓的全套 cna/tfstk/isg…）时原样带上。
func headers(acct *store.Account, base, chatID string) map[string]string {
	sum := sha256.Sum256([]byte("qwen-umid:" + acct.AccessToken))
	umid := hex.EncodeToString(sum[:])[:64]
	if v := acct.Extra["bxUmid"]; v != "" {
		umid = v
	}
	bxUa := userAgent
	if v := acct.Extra["bxUa"]; v != "" {
		bxUa = v
	}
	bxVVal := bxV
	if v := acct.Extra["bxV"]; v != "" {
		bxVVal = v
	}
	ver := version
	if v := acct.Extra["siteVer"]; v != "" {
		ver = v
	}
	referer := base + "/"
	if chatID != "" {
		referer = base + "/c/" + chatID
	}
	h := map[string]string{
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
		"bx-v":               bxVVal,
		"bx-umidtoken":       umid,
		"bx-ua":              bxUa,
		"timezone":           timezone,
		"version":            ver,
		"x-request-origin":   base,
		"x-request-id":       provider.NewUUID(),
		"x-accel-buffering":  "no",
		"authorization":      "Bearer " + acct.AccessToken,
	}
	if acct.Extra["wafCookie"] != "" {
		h["cookie"] = acct.Extra["wafCookie"]
	}
	return h
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

// Refresh 在 profile.go：校验令牌，失效时从浏览器配置目录重读 cookie 续期。

// ── 模型 ──

// seedModels 上游目录不可用时的兜底模型表（2026-10 实测目录）。
var seedModels = []provider.ModelInfo{
	{ID: "qwen3.8-max"}, {ID: "qwen3.7-plus"}, {ID: "qwen3.8-omni-flash"},
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
	model     string          // 去掉功能后缀的基础模型
	content   string          // 折叠后的完整 user content
	thinking  bool            // 是否启用思考
	toolNames map[string]bool // 请求声明的工具名（nil=未启用工具，桥接只出不转发）
}

// promptChars 上游不回 usage 时的粗估依据。
func (r *parsedReq) promptChars() int { return len([]rune(r.content)) }

// parseReq 把 OpenAI 请求折叠为千问网页版的单条 user 消息
// （多轮用 <｜User｜>/<｜Assistant｜> 特殊标记折叠，移植自 Qwen2OpenAI 实测协议）。
func parseReq(openaiReq []byte, defaultModel string) (*parsedReq, error) {
	var src struct {
		Model      string            `json:"model"`
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
		Messages   []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
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
	callIDToName := map[string]string{}
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
		case "tool":
			// 工具结果 → <tool_response> 用户消息（协议见 tools.go）
			flushUser()
			name := m.Name
			if name == "" {
				name = callIDToName[m.ToolCallID]
			}
			if name == "" {
				name = "tool"
			}
			conv = append(conv, fmt.Sprintf("<tool_response tool_call_id=%q name=%q>\n%s\n</tool_response>", m.ToolCallID, name, text))
		case "assistant":
			flushUser()
			// 历史 assistant 的 tool_calls 还原成 <tool_call> 块，让模型看懂自己的调用
			if len(m.ToolCalls) > 0 {
				var blocks []string
				if text != "" {
					blocks = append(blocks, text)
				}
				for _, tc := range m.ToolCalls {
					callIDToName[tc.ID] = tc.Function.Name
					// OpenAI 的 arguments 是「内容为 JSON 的字符串」，解出原文再嵌进 <tool_call>
					args := string(tc.Function.Arguments)
					var s string
					if json.Unmarshal(tc.Function.Arguments, &s) == nil && s != "" {
						args = s
					}
					if args == "" {
						args = "{}"
					}
					blocks = append(blocks, fmt.Sprintf("%s\n{\"name\": %q, \"arguments\": %s}\n%s", tcOpen, tc.Function.Name, args, tcClose))
				}
				conv = append(conv, fmt.Sprintf("<｜Assistant｜>%s<｜end of sentence｜>", strings.Join(blocks, "\n")))
			} else {
				conv = append(conv, fmt.Sprintf("<｜Assistant｜>%s<｜end of sentence｜>", text))
			}
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
	// 工具桥接：有 tools 时把协议说明并进 system（网页版无原生 tools 参数）
	if len(src.Tools) > 0 {
		mustCall := string(src.ToolChoice) == `"required"`
		var named map[string]any
		if json.Unmarshal(src.ToolChoice, &named) == nil {
			if fn, ok := named["function"].(map[string]any); ok {
				if _, ok := fn["name"].(string); ok {
					mustCall = true
				}
			}
		}
		if prompt := buildToolSystemPrompt(src.Tools, mustCall); prompt != "" {
			if system != "" {
				system += "\n\n"
			}
			system += prompt
			if content != "" {
				content = system + "\n\n" + content
			} else {
				content = system
			}
		}
	}
	return &parsedReq{model: base, content: content, thinking: thinking, toolNames: toolNamesFromTools(src.Tools)}, nil
}

// toolNamesFromTools 从 OpenAI tools 数组抽取函数名集合（nil 表示未启用工具）。
func toolNamesFromTools(tools []json.RawMessage) map[string]bool {
	if len(tools) == 0 {
		return nil
	}
	names := map[string]bool{}
	for _, raw := range tools {
		var m struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
			Name string `json:"name"` // 兼容平铺格式
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if n := m.Function.Name; n != "" {
			names[n] = true
		} else if m.Name != "" {
			names[m.Name] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
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

// tokenExpiring access token 是否已过期或 90 秒内即将过期（千问 access token 约 15 分钟寿命）。
func tokenExpiring(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false // 非 JWT 无法判断，交给 401→刷新链路
	}
	pl, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(pl, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return time.Now().Unix()+90 >= claims.Exp
}

// wafPunish 识别阿里系 WAF 风控拦截页（RGV587 / _____tmd_____ / punish），
// 实测响应形如 {"ret":["FAIL_SYS_USER_VALIDATE","RGV587_ERROR::SM::哎哟喂,…"],
// "data":{"url":"https://chat.qwen.ai:443//api/v2/…/_____tmd_____/punish?x5secdata=…"}}。
func wafPunish(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := string(body)
	return strings.Contains(s, "RGV587_ERROR") ||
		strings.Contains(s, "FAIL_SYS_USER_VALIDATE") ||
		strings.Contains(s, "_____tmd_____") ||
		strings.Contains(s, "punish?x5secdata")
}

// rePunishURL 从拦截响应里抠出 punish 页地址（带 x5secdata 令牌），
// 面板用它打开滑块验证页，通过后种下 x5sec cookie 即可解除风控。
var rePunishURL = regexp.MustCompile(`https?://[^"\\\s]+?/_____tmd_____/punish\?[^"\\\s]+`)

// OnPunish 风控回调：宿主（面板层）启动时注入，收到账号 ID 与 punish 页地址。
// 回调里只做轻量存储写入，不要阻塞。
var OnPunish func(acctID, punishURL string)

// notePunish 上报一次风控拦截（从响应体提取 punish 地址）。
func notePunish(acct *store.Account, body []byte) {
	if OnPunish == nil || len(body) == 0 {
		return
	}
	if m := rePunishURL.Find(body); len(m) > 0 {
		OnPunish(acct.ID, string(m))
	}
}

// Chat 实现 provider.Provider。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	base := p.base()
	// access token 15 分钟到期：临期先换新，避免每次都先吃一个 401 再由池子补
	if tokenExpiring(acct.AccessToken) {
		_ = p.exchangeAcct(ctx, acct)
	}
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
			ID      string `json:"id"`
			Code    string `json:"code"`
			Details string `json:"details"`
			URL     string `json:"url"` // WAF punish 页地址（x5secdata 令牌）
		} `json:"data"`
		Message string   `json:"message"`
		Ret     []string `json:"ret"` // 阿里 WAF 拦截页标记（FAIL_SYS_USER_VALIDATE）
	}
	nb, _ := json.Marshal(newChat)
	status, err := httpx.DoJSON(ctx, "POST", base+"/api/v2/chats/new", h, nb, &created, 30*time.Second)
	if err != nil {
		sink.Close()
		if status == 401 || status == 403 {
			return provider.Err(provider.KindAuth, status, "千问令牌已过期：请在面板对千问账号点「刷新」，失效则到添加页重新一键浏览器导入登录")
		}
		// WAF 风控会返回 HTML 验证页（DoJSON 解码失败的 body 片段里可见）
		if es := err.Error(); wafPunish([]byte(es)) || strings.Contains(es, "invalid character '<'") || strings.Contains(strings.ToLower(es), "<!doctype") {
			notePunish(acct, []byte(es))
			return provider.Err(provider.KindCaptcha, status, "千问触发了风控人机验证：请在面板千问账号上点「🛡 过验证」完成滑块")
		}
		return provider.Err(provider.KindNetwork, 0, "新建会话失败: %v", err)
	}
	if status >= 400 {
		sink.Close()
		if status == 401 || status == 403 {
			return provider.Err(provider.KindAuth, status, "千问令牌已过期：请在面板对千问账号点「刷新」，失效则到添加页重新一键浏览器导入登录")
		}
		return provider.ClassifyBody(status, nil)
	}
	chatID := created.Data.ID
	if chatID == "" {
		sink.Close()
		// 阿里 WAF 拦截页是合法 JSON（ret=[FAIL_SYS_USER_VALIDATE,…]），会解码成功但拿不到 ID
		if len(created.Ret) > 0 && wafPunish([]byte(strings.Join(created.Ret, "\n"))) {
			if created.Data.URL != "" {
				notePunish(acct, []byte(created.Data.URL))
			} else {
				notePunish(acct, []byte(created.Message))
			}
			return provider.Err(provider.KindCaptcha, 200, "千问触发了风控人机验证：请在面板千问账号上点「🛡 过验证」完成滑块")
		}
		detail := created.Data.Details
		if detail == "" {
			detail = created.Data.Code
		}
		if detail == "" {
			detail = created.Message
		}
		if strings.Contains(strings.ToLower(detail), "unauthorized") || strings.Contains(detail, "expired") {
			// 明确的鉴权失败 → 归为 KindAuth，让账号池走「刷新凭据→原号重试」
			return provider.Err(provider.KindAuth, 200, "千问令牌已过期")
		}
		if detail != "" {
			return provider.Err(provider.KindServer, 0, "新建会话被上游拒绝: %s", detail)
		}
		return provider.Err(provider.KindServer, 0, "新建会话失败：上游未返回会话 ID（可能风控拦截），请稍后重试")
	}
	// 2) 发送对话（SSE）
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
	// 上游偶发 200 + 空 JSON 体（风控/瞬时故障，实测 2026-10-18）：没写出任何
	// 内容时原样重试一次，避免单账号直接把 502 抛给客户端。
	var lastSendErr *provider.Error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(1200 * time.Millisecond):
			case <-ctx.Done():
				sink.Close()
				return provider.Err(provider.KindNetwork, 0, "%v", ctx.Err())
			}
		}
		// 每次尝试换新的 x-request-id（headers 按 token 稳定派生 umid，不受影响）
		h := headers(acct, base, chatID)
		resp, err := httpx.DoStream(ctx, "POST", base+"/api/v2/chat/completions?chat_id="+chatID, h, pb, p.cfg.ConnectTimeout())
		if err != nil {
			sink.Close()
			return provider.Err(provider.KindNetwork, 0, "%v", err)
		}
		if resp.Status >= 400 {
			sink.Close()
			if wafPunish(resp.Body) {
				notePunish(acct, resp.Body)
				return provider.Err(provider.KindCaptcha, resp.Status, "千问触发了风控人机验证：请在面板千问账号上点「🛡 过验证」完成滑块")
			}
			return provider.ClassifyBody(resp.Status, resp.Body)
		}
		if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
			// 2xx + JSON：不是 SSE。此前直接丢流分类导致真实错误体被报成
			// "(empty body)"——先读出 body（上限 1MB）再分类，含鉴权字样时
			// 池子还能走刷新→重试链路。
			body, _ := io.ReadAll(io.LimitReader(resp.Stream, 1<<20))
			resp.Stream.Close()
			resp.Stream = nil
			if wafPunish(body) {
				sink.Close()
				notePunish(acct, body)
				return provider.Err(provider.KindCaptcha, resp.Status, "千问触发了风控人机验证：请在面板千问账号上点「🛡 过验证」完成滑块")
			}
			if strings.TrimSpace(string(body)) != "" {
				sink.Close()
				return provider.ClassifyBody(resp.Status, body)
			}
			lastSendErr = provider.Err(provider.KindServer, 200, "千问上游返回空响应（可能风控拦截或瞬时故障），请稍后重试")
			continue
		}
		return parseSSE(resp.Stream, sink, req.promptChars(), req.toolNames)
	}
	sink.Close()
	if lastSendErr != nil {
		return lastSendErr
	}
	return provider.Err(provider.KindServer, 0, "对话发送失败")
}

// parseSSE 解析千问网页版 SSE（phase 分帧）。
func parseSSE(r io.Reader, sink provider.Sink, promptChars int, toolNames map[string]bool) error {
	defer sink.Close()
	firstRoleSent := false
	hasDelta := false
	started := false
	finishReason := ""
	var usage *provider.Usage
	var thinkChars int
	tcp := newToolCallParser(toolNames)

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
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				TotalTokens  int64 `json:"total_tokens"`
			} `json:"usage"`
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
				thinkChars += len([]rune(content))
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
				// 文本 → 工具调用桥接：<tool_call> 块翻译成 OpenAI tool_calls 增量
				textOut, calls := tcp.push(content)
				if textOut != "" {
					delta := provider.Delta{Content: textOut}
					if !firstRoleSent {
						delta.Role = "assistant"
						firstRoleSent = true
					}
					hasDelta = true
					sink.Delta(delta)
				}
				for _, c := range calls {
					delta := provider.Delta{Tool: &provider.ToolDelta{Index: c.Index, ID: c.ID, Name: c.Name, Args: c.Args}}
					if !firstRoleSent {
						delta.Role = "assistant"
						firstRoleSent = true
					}
					hasDelta = true
					sink.Delta(delta)
				}
			}
			if d.Status == "finished" {
				finishReason = "stop"
			}
		}
		if fr, ok := frame.Choices[0].FinishReason.(string); ok && fr != "" {
			finishReason = fr
		}
		// usage 帧（可能在任意帧上，取最后一次非零值）
		if frame.Usage != nil && (frame.Usage.InputTokens > 0 || frame.Usage.OutputTokens > 0) {
			u := frame.Usage
			total := u.TotalTokens
			if total == 0 {
				total = u.InputTokens + u.OutputTokens
			}
			usage = &provider.Usage{
				PromptTokens:     u.InputTokens,
				CompletionTokens: u.OutputTokens,
				TotalTokens:      total,
				Present:          true,
			}
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
	// 收尾：吐出扣住的文本/未闭合调用；发过工具调用则 finish_reason=tool_calls
	if tail, calls := tcp.flush(); tail != "" || len(calls) > 0 {
		if tail != "" {
			delta := provider.Delta{Content: tail}
			if !firstRoleSent {
				delta.Role = "assistant"
			}
			sink.Delta(delta)
		}
		for _, c := range calls {
			sink.Delta(provider.Delta{Tool: &provider.ToolDelta{Index: c.Index, ID: c.ID, Name: c.Name, Args: c.Args}})
		}
	}
	if usage == nil {
		// 上游未回 usage：按字符数粗估（中文 ≈0.6 token/字，英文混合再打折），聊胜于无
		prompt := int64(float64(promptChars) * 0.6)
		completion := int64(float64(thinkChars) * 0.6)
		if prompt < 1 {
			prompt = 1
		}
		if completion < 1 {
			completion = 1
		}
		usage = &provider.Usage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      prompt + completion,
			Present:          true, // 估算值也计入台账，避免“用了但不计数”
		}
	}
	// 桥接过工具调用 → OpenAI 语义的 finish_reason
	if tcp.emittedCount > 0 {
		finishReason = "tool_calls"
	}
	sink.Finish(provider.Finish{Reason: finishReason, Usage: usage})
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
