// DeepSeek 网页版（chat.deepseek.com）反代：
// 浏览器登录态 userToken（localStorage）作为凭据，对话走
// /api/v0/chat/completion（每次请求前领 PoW 挑战并求解）。
// 无原生工具能力，带工具的请求由网关伪工具协议处理（gateway.pseudoProviders）。
package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const defaultBase = "https://chat.deepseek.com"

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"

// Provider DeepSeek 网页版提供商。
type Provider struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Provider {
	// 连接预热：对话上游热连接，请求时免握手
	httpx.RegisterWarm(baseOf(cfg))
	return &Provider{cfg: cfg}
}

func baseOf(cfg *config.Config) string {
	if pc := cfg.PC(store.PDeepSeek); pc != nil && pc.APIBase != "" {
		return strings.TrimRight(pc.APIBase, "/")
	}
	return defaultBase
}

func (p *Provider) base() string { return baseOf(p.cfg) }

func (p *Provider) Name() string { return store.PDeepSeek }

// headers 浏览器形状请求头。powHeader 非空时带上工作量证明。
func headers(acct *store.Account, base, powHeader string) map[string]string {
	// 网页前端发 |getTimezoneOffset()|*60（本地时区偏移秒数）
	_, offset := time.Now().Zone()
	if offset < 0 {
		offset = -offset
	}
	h := map[string]string{
		"accept":                   "application/json, text/event-stream",
		"accept-language":          "zh-CN,zh;q=0.9",
		"content-type":             "application/json",
		"user-agent":               userAgent,
		"origin":                   base,
		"referer":                  base + "/",
		"x-client-platform":        "web",
		"x-client-version":         "2.0.0",
		"x-client-locale":          "zh-CN",
		"x-client-timezone-offset": fmt.Sprintf("%d", offset),
		"x-app-version":            "2.0.0",
		"x-request-id":             provider.NewUUID(),
		"authorization":            "Bearer " + acct.AccessToken,
	}
	if powHeader != "" {
		h["x-ds-pow-response"] = powHeader
	}
	return h
}

// ── 模型 ──

// seedModels 网页版无模型目录 API：固定两个入口，思考用 -thinking 后缀微调。
var seedModels = []provider.ModelInfo{
	{ID: "deepseek-chat", Name: "DeepSeek-V4.1-Flash", Tags: []string{"网页版免费|#2dd4bf"}},
	{ID: "deepseek-reasoner", Name: "DeepSeek-V4.1-Flash 思考", Reasoning: true, Tags: []string{"网页版免费|#2dd4bf"}},
}

func (p *Provider) SeedModels() []provider.ModelInfo { return seedModels }

// Models 网页版无目录接口：直接回固定清单（与 SeedModels 一致）。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	return seedModels, nil
}

// ── 账号信息 ──

// fetchUser 校验令牌并取账号信息（GET /api/v0/users/current）。
func (p *Provider) fetchUser(ctx context.Context, acct *store.Account) (map[string]any, error) {
	var out map[string]any
	status, err := httpx.DoJSON(ctx, "GET", p.base()+"/api/v0/users/current", headers(acct, p.base(), ""), nil, &out, 30*time.Second)
	if err != nil {
		if status == 401 || status == 403 {
			return nil, provider.Err(provider.KindAuth, status, "DeepSeek 令牌无效或已过期")
		}
		return nil, provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	if code, _ := out["code"].(float64); code != 0 && code != 200 {
		msg, _ := out["msg"].(string)
		return nil, provider.Err(provider.KindAuth, int(code), "DeepSeek 令牌无效: %s", msg)
	}
	if status >= 400 {
		return nil, provider.ClassifyBody(status, nil)
	}
	return out, nil
}

// Quota 网页版无额度接口：校验令牌并返回用户信息型快照。
func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	user, err := p.fetchUser(ctx, acct)
	if err != nil {
		return nil, err
	}
	data, _ := user["data"].(map[string]any)
	biz, _ := data["biz_data"].(map[string]any)
	name := ""
	if biz != nil {
		name, _ = biz["nickname"].(string)
	}
	if name == "" && acct.Nickname == "" {
		name = "DeepSeek 账号"
	}
	if name != "" && acct.Nickname == "" {
		acct.Nickname = name
	}
	return &store.QuotaSnapshot{
		Source:    "userinfo",
		Plan:      "DeepSeek Web",
		Note:      "网页版免费，无额度接口，令牌有效",
		UpdatedAt: time.Now().Unix(),
	}, nil
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return false }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}

// Refresh 网页版令牌无刷新机制：校验有效性，失效只能重新浏览器导入。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	_, err := p.fetchUser(ctx, acct)
	return err
}

// ── 请求折叠 ──

// parsedReq 解析后的对话请求。
type parsedReq struct {
	model    string
	content  string
	thinking bool
}

// promptChars 上游不回 usage 时的粗估依据。
func (r *parsedReq) promptChars() int { return len([]rune(r.content)) }

// parseReq 把 OpenAI 请求折叠为网页版的单条 user prompt
// （多轮用 <｜User｜>/<｜Assistant｜> 特殊标记折叠，与 qwen provider 同构；
// 历史 tool_calls 还原成 <tool_call> 标记块，与网关伪工具协议同格式）。
func parseReq(openaiReq []byte, defaultModel string) (*parsedReq, error) {
	var src struct {
		Model    string `json:"model"`
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
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
	if model == "" {
		model = "deepseek-chat"
	}
	thinking := strings.Contains(model, "reasoner") || strings.Contains(model, "r1")
	switch {
	case strings.HasSuffix(model, "-thinking"):
		model = strings.TrimSuffix(model, "-thinking")
		thinking = true
	case strings.HasSuffix(model, "-no-thinking"):
		model = strings.TrimSuffix(model, "-no-thinking")
		thinking = false
	}
	extract := func(v any) string {
		s, ok := v.(string)
		if ok {
			return s
		}
		// 多模态数组：取全部 text 部分
		if parts, ok := v.([]any); ok {
			var sb strings.Builder
			for _, pt := range parts {
				if m, ok := pt.(map[string]any); ok {
					if t, _ := m["type"].(string); t == "text" {
						if txt, _ := m["text"].(string); txt != "" {
							if sb.Len() > 0 {
								sb.WriteByte('\n')
							}
							sb.WriteString(txt)
						}
					}
				}
			}
			return sb.String()
		}
		return ""
	}
	var system string
	var conv []string
	callIDToName := map[string]string{}
	for _, m := range src.Messages {
		text := extract(m.Content)
		switch m.Role {
		case "system", "developer":
			if system != "" {
				system += "\n\n"
			}
			system += text
		case "user":
			conv = append(conv, text)
		case "tool":
			name := m.Name
			if name == "" {
				name = callIDToName[m.ToolCallID]
			}
			if name == "" {
				name = "tool"
			}
			conv = append(conv, fmt.Sprintf("<tool_response tool_call_id=%q name=%q>\n%s\n</tool_response>", m.ToolCallID, name, text))
		case "assistant":
			if len(m.ToolCalls) > 0 {
				var blocks []string
				if text != "" {
					blocks = append(blocks, text)
				}
				for _, tc := range m.ToolCalls {
					callIDToName[tc.ID] = tc.Function.Name
					args := string(tc.Function.Arguments)
					var s string
					if json.Unmarshal(tc.Function.Arguments, &s) == nil && s != "" {
						args = s
					}
					if args == "" {
						args = "{}"
					}
					blocks = append(blocks, fmt.Sprintf("<tool_call>\n{\"name\": %q, \"arguments\": %s}\n</tool_call>", tc.Function.Name, args))
				}
				conv = append(conv, fmt.Sprintf("<｜Assistant｜>%s<｜end of sentence｜>", strings.Join(blocks, "\n")))
			} else {
				conv = append(conv, fmt.Sprintf("<｜Assistant｜>%s<｜end of sentence｜>", text))
			}
		}
	}
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
	return &parsedReq{model: model, content: content, thinking: thinking}, nil
}

// ── 上游调用 ──

// serverBusy 高峰期「服务器繁忙，请稍后再试」（负载限制非额度）→ 限流类，池子换号重试。
var reBusy = regexp.MustCompile(`服务器繁忙|server is busy|请稍后再试|rate.?limit`)

// createSession 新建网页会话。
func (p *Provider) createSession(ctx context.Context, acct *store.Account) (string, error) {
	var out struct {
		Code float64 `json:"code"`
		Msg  string  `json:"msg"`
		Data struct {
			BizData struct {
				ID          string `json:"id"`
				ChatSession struct {
					ID string `json:"id"`
				} `json:"chat_session"`
			} `json:"biz_data"`
		} `json:"data"`
	}
	b, _ := json.Marshal(map[string]any{})
	status, err := httpx.DoJSON(ctx, "POST", p.base()+"/api/v0/chat_session/create", headers(acct, p.base(), ""), b, &out, 30*time.Second)
	if err != nil {
		if status == 401 || status == 403 {
			return "", provider.Err(provider.KindAuth, status, "DeepSeek 令牌无效或已过期")
		}
		return "", provider.Err(provider.KindNetwork, 0, "新建会话失败: %v", err)
	}
	if status >= 400 {
		return "", provider.ClassifyBody(status, nil)
	}
	if out.Code != 0 && out.Code != 200 {
		if out.Code == 40002 || out.Code == 40003 {
			return "", provider.Err(provider.KindAuth, int(out.Code), "DeepSeek 令牌无效: %s", out.Msg)
		}
		return "", provider.Err(provider.KindServer, int(out.Code), "新建会话被上游拒绝: %s", out.Msg)
	}
	id := out.Data.BizData.ChatSession.ID
	if id == "" {
		id = out.Data.BizData.ID
	}
	if id == "" {
		return "", provider.Err(provider.KindServer, 0, "新建会话失败：上游未返回会话 ID")
	}
	return id, nil
}

// deleteSession 删除网页会话（best-effort：失败静默，不影响对话结果）。
// 网页版删除接口幂等，会话不存在也返回 code:0。
func (p *Provider) deleteSession(ctx context.Context, acct *store.Account, sessionID string) {
	body, _ := json.Marshal(map[string]string{"chat_session_id": sessionID})
	var out map[string]any
	_, _ = httpx.DoJSON(ctx, "POST", p.base()+"/api/v0/chat_session/delete", headers(acct, p.base(), ""), body, &out, 10*time.Second)
}

// fetchChallenge 领 PoW 挑战。
func (p *Provider) fetchChallenge(ctx context.Context, acct *store.Account) (Challenge, error) {
	var out struct {
		Code float64 `json:"code"`
		Msg  string  `json:"msg"`
		Data struct {
			BizData map[string]any `json:"biz_data"`
		} `json:"data"`
	}
	body, _ := json.Marshal(map[string]string{"target_path": "/api/v0/chat/completion"})
	status, err := httpx.DoJSON(ctx, "POST", p.base()+"/api/v0/chat/create_pow_challenge", headers(acct, p.base(), ""), body, &out, 30*time.Second)
	if err != nil {
		if status == 401 || status == 403 {
			return Challenge{}, provider.Err(provider.KindAuth, status, "DeepSeek 令牌无效或已过期")
		}
		return Challenge{}, provider.Err(provider.KindNetwork, 0, "领 PoW 挑战失败: %v", err)
	}
	if status >= 400 {
		return Challenge{}, provider.ClassifyBody(status, nil)
	}
	if out.Code != 0 && out.Code != 200 {
		if out.Code == 40002 || out.Code == 40003 {
			return Challenge{}, provider.Err(provider.KindAuth, int(out.Code), "DeepSeek 令牌无效: %s", out.Msg)
		}
		return Challenge{}, provider.Err(provider.KindServer, int(out.Code), "领 PoW 挑战被拒绝: %s", out.Msg)
	}
	m := out.Data.BizData
	if sub, ok := m["challenge"].(map[string]any); ok {
		m = sub
	}
	ch := Challenge{
		Algorithm:  strField(m, "algorithm", "DeepSeekHashV1"),
		Challenge:  strField(m, "challenge", ""),
		Salt:       strField(m, "salt", ""),
		Signature:  strField(m, "signature", ""),
		TargetPath: strField(m, "target_path", "/api/v0/chat/completion"),
		Difficulty: numField(m, "difficulty", 144000),
		ExpireAt:   numField(m, "expire_at", 0),
	}
	if ch.Challenge == "" {
		return Challenge{}, provider.Err(provider.KindServer, 0, "PoW 挑战为空（可能风控或限流），请稍后重试")
	}
	return ch, nil
}

func strField(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func numField(m map[string]any, key string, def int64) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return def
}

// Chat 实现 provider.Provider。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	req, err := parseReq(openaiReq, p.cfg.PC(store.PDeepSeek).DefaultModel)
	if err != nil {
		sink.Close()
		return err
	}
	// 1) 新建会话
	sessionID, err := p.createSession(ctx, acct)
	if err != nil {
		sink.Close()
		return err
	}
	// 网页无痕模式（默认开）：对话结束（含失败路径）后删除会话，
	// 避免网页版历史被 API 调用堆积。用独立 context，上游流被客户端取消时也能完成删除。
	if p.cfg.CleanWebHistoryOn() {
		defer func() {
			dctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			p.deleteSession(dctx, acct, sessionID)
		}()
	}
	// 2) 领挑战并求解 PoW
	ch, err := p.fetchChallenge(ctx, acct)
	if err != nil {
		sink.Close()
		return err
	}
	powHeader, err := SolvePow(ch)
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindServer, 0, "PoW 求解失败: %v", err)
	}
	// 3) 发送对话（SSE）
	payload := map[string]any{
		"chat_session_id":   sessionID,
		"parent_message_id": nil,
		"model_type":        "default",
		"prompt":            req.content,
		"ref_file_ids":      []string{},
		"thinking_enabled":  req.thinking,
		"search_enabled":    false,
		"action":            nil,
		"preempt":           false,
	}
	pb, _ := json.Marshal(payload)
	h := headers(acct, p.base(), powHeader)
	h["accept"] = "text/event-stream"
	resp, err := httpx.DoStream(ctx, "POST", p.base()+"/api/v0/chat/completion", h, pb, p.cfg.ConnectTimeout())
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	if resp.Status >= 400 {
		sink.Close()
		if reBusy.MatchString(string(resp.Body)) {
			return provider.Err(provider.KindRate, resp.Status, "DeepSeek 服务器繁忙，请稍后再试")
		}
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		body, _ := io.ReadAll(io.LimitReader(resp.Stream, 1<<20))
		resp.Stream.Close()
		resp.Stream = nil
		if reBusy.MatchString(string(body)) {
			sink.Close()
			return provider.Err(provider.KindRate, resp.Status, "DeepSeek 服务器繁忙，请稍后再试")
		}
		sink.Close()
		return provider.ClassifyBody(resp.Status, body)
	}
	return parseSSE(resp.Stream, sink, req)
}

// parseSSE 解析 DeepSeek 网页版 SSE（{p,o,v} JSON patch + 裸增量）。
func parseSSE(r io.Reader, sink provider.Sink, req *parsedReq) error {
	defer sink.Close()
	firstRoleSent := false
	hasDelta := false
	finishReason := ""
	var usage *provider.Usage
	var thinkChars int
	p := &streamParser{}

	var streamErr *provider.Error
	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		switch ev.Event {
		case "close", "keep-alive", "title", "update_session":
			return true
		case "hint":
			// 错误提示走 hint 事件：{"type":"error","content":"..."}
			var raw struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(ev.Data), &raw) == nil && raw.Type == "error" {
				msg := raw.Content
				if msg == "" {
					msg = "DeepSeek 上游错误"
				}
				// 未吐过内容时返回分类错误（池子可换号重试）；已吐内容只能流内报错
				if !hasDelta {
					if reBusy.MatchString(msg) {
						streamErr = provider.Err(provider.KindRate, 200, "DeepSeek 服务器繁忙，请稍后再试")
					} else {
						streamErr = provider.Err(provider.KindStream, 200, "DeepSeek 上游错误: %s", msg)
					}
				} else {
					sink.StreamError(0, msg)
				}
				return false
			}
			return true
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			if data == "[DONE]" {
				finishReason = "stop"
			}
			return true
		}
		var raw map[string]any
		if json.Unmarshal([]byte(data), &raw) != nil {
			return true
		}
		// OpenAI delta 形状兜底（部分版本接口）
		if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
					finishReason = fr
				}
				delta, _ := choice["delta"].(map[string]any)
				content, _ := delta["content"].(string)
				reasoning, _ := delta["reasoning_content"].(string)
				if reasoning != "" || content != "" {
					p.emit(sink, &firstRoleSent, &hasDelta, reasoning, content, &thinkChars)
				}
				return true
			}
		}
		// DeepSeek 网页 JSON patch 形状
		chunk := p.applyPatch(raw)
		if chunk != nil {
			if chunk.finish {
				finishReason = "stop"
				return true
			}
			p.emit(sink, &firstRoleSent, &hasDelta, chunk.reasoning, chunk.content, &thinkChars)
		}
		return true
	})
	if streamErr != nil {
		return streamErr
	}
	if err != nil {
		return err
	}
	if !hasDelta {
		sink.StreamError(0, "DeepSeek 上游返回空流（令牌可能已失效或触发风控）")
		return nil
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if usage == nil {
		// 上游未回 usage：按字符数粗估，聊胜于无
		prompt := int64(float64(req.promptChars()) * 0.6)
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
			Present:          true,
		}
	}
	sink.Finish(provider.Finish{Reason: finishReason, Usage: usage})
	return nil
}

// busyErr SSE 内提前终止时携带的分类错误（见 hint 分支，已改为闭包内局部变量）。

// emit 把一段文本推给 sink（reasoning 或 content）。
func (p *streamParser) emit(sink provider.Sink, firstRoleSent, hasDelta *bool, reasoning, content string, thinkChars *int) {
	if reasoning != "" {
		*thinkChars += len([]rune(reasoning))
		delta := provider.Delta{Reasoning: reasoning}
		if !*firstRoleSent {
			delta.Role = "assistant"
			*firstRoleSent = true
		}
		*hasDelta = true
		sink.Delta(delta)
	}
	if content != "" {
		delta := provider.Delta{Content: content}
		if !*firstRoleSent {
			delta.Role = "assistant"
			*firstRoleSent = true
		}
		*hasDelta = true
		sink.Delta(delta)
	}
}

// streamChunk 单帧解析产物。
type streamChunk struct {
	content   string
	reasoning string
	finish    bool
}

// streamParser 网页 SSE 有状态解析器：p 路径与当前片段类型跨帧保持。
type streamParser struct {
	path         string
	fragmentType string
}

const fragmentContentPath = "response/fragments/-1/content"

func (p *streamParser) applyPatch(data map[string]any) *streamChunk {
	if pv, ok := data["p"].(string); ok && pv != "" {
		p.path = pv
	}
	val := data["v"]
	// 快照对象：{"v": {"response": {"message_id":..., "fragments": [...]}}}
	if vMap, ok := val.(map[string]any); ok {
		if respMap, ok := vMap["response"].(map[string]any); ok {
			if frags, ok := respMap["fragments"].([]any); ok {
				return p.parseFragments(frags)
			}
		}
	}
	// 片段列表追加：{"p":"response/fragments","v":[{type,content}]}
	if p.path == "response/fragments" {
		if frags, ok := val.([]any); ok {
			p.path = fragmentContentPath
			return p.parseFragments(frags)
		}
	}
	// 当前片段正文增量：{"v":"text"}
	if p.path == fragmentContentPath {
		if text, ok := val.(string); ok {
			return p.textChunk(p.fragmentType, text)
		}
	}
	// 旧版直通路径
	if p.path == "response/content" || p.path == "response/thinking_content" {
		if text, ok := val.(string); ok {
			t := "RESPONSE"
			if strings.Contains(p.path, "thinking") {
				t = "THINK"
			}
			return p.textChunk(t, text)
		}
	}
	if p.path == "response/status" {
		if s, ok := val.(string); ok && s == "FINISHED" {
			return &streamChunk{finish: true}
		}
	}
	return nil
}

func (p *streamParser) parseFragments(frags []any) *streamChunk {
	var content, reasoning strings.Builder
	for _, item := range frags {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := m["type"].(string); ok && t != "" {
			p.fragmentType = t
		}
		text, ok := m["content"].(string)
		if !ok || text == "" {
			continue
		}
		switch p.fragmentType {
		case "THINK", "THINKING", "REASONING":
			reasoning.WriteString(text)
		case "RESPONSE", "TEXT", "SEARCH":
			content.WriteString(text)
		}
	}
	if content.Len() == 0 && reasoning.Len() == 0 {
		return nil
	}
	return &streamChunk{content: content.String(), reasoning: reasoning.String()}
}

func (p *streamParser) textChunk(fType, text string) *streamChunk {
	switch fType {
	case "THINK", "THINKING", "REASONING":
		return &streamChunk{reasoning: text}
	default:
		return &streamChunk{content: text}
	}
}
