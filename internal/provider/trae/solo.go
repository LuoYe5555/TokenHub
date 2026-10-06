package trae

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// ── SOLO 请求体改写（移植自 trae2api-web payload.go） ──

func prepareSOLO(openaiReq []byte, defaultModel string) (map[string]any, error) {
	var src map[string]any
	if err := json.Unmarshal(openaiReq, &src); err != nil {
		return nil, provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	if src == nil {
		src = map[string]any{}
	}
	b := src
	b["stream"] = true
	b["function"] = Function

	model, _ := b["model"].(string)
	if strings.TrimSpace(model) == "" {
		model = defaultModel
	}
	b["model"] = model
	b["config_name"] = model

	if msgs, ok := b["messages"].([]any); ok {
		b["messages"] = normalizeMessages(msgs)
	}
	if tc, ok := b["tool_choice"]; ok {
		normalized, drop := normalizeToolChoice(tc)
		if drop {
			delete(b, "tool_choice")
			delete(b, "tools")
			delete(b, "functions")
		} else if normalized != nil {
			b["tool_choice"] = normalized
		} else {
			delete(b, "tool_choice")
		}
	}
	if tools, ok := b["tools"].([]any); ok {
		for _, tv := range tools {
			t, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := t["function"].(map[string]any)
			if !ok {
				continue
			}
			// SOLO 的 parameters 是 JSON 字符串而非对象
			if params, ok := fn["parameters"]; ok {
				if _, isStr := params.(string); !isStr {
					raw, err := json.Marshal(params)
					if err == nil {
						fn["parameters"] = string(raw)
					}
				}
			}
		}
	}
	return b, nil
}

func normalizeMessages(msgs []any) []any {
	out := make([]any, 0, len(msgs))
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			out = append(out, mv)
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "developer":
			m["role"] = "system"
		case "assistant":
			if tcs, ok := m["tool_calls"].([]any); ok {
				converted := make([]any, 0, len(tcs))
				for _, tc := range tcs {
					c, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := c["function"].(map[string]any)
					if fn == nil {
						continue
					}
					name, _ := fn["name"].(string)
					if name == "" {
						continue
					}
					converted = append(converted, map[string]any{
						"id":            c["id"],
						"type":          c["type"],
						"function_call": fn,
					})
				}
				if len(converted) > 0 {
					m["tool_calls"] = converted
				} else {
					delete(m, "tool_calls")
				}
			}
		}
		// content string → [{type:text,text}]
		if s, ok := m["content"].(string); ok {
			m["content"] = []any{map[string]any{"type": "text", "text": s}}
		}
		out = append(out, m)
	}
	return out
}

func normalizeToolChoice(v any) (normalized any, drop bool) {
	switch t := v.(type) {
	case string:
		if t == "none" {
			return nil, true
		}
		return t, false
	case map[string]any:
		typ, _ := t["type"].(string)
		switch typ {
		case "none":
			return nil, true
		case "function":
			if fn, ok := t["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok && name != "" {
					return name, false
				}
			}
			return "auto", false
		default:
			return typ, false
		}
	}
	return "auto", false
}

// ── Chat ──

func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	pc := p.cfg.PC(store.PTrae)
	body, err := prepareSOLO(openaiReq, pc.DefaultModel)
	if err != nil {
		sink.Close()
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		sink.Close()
		return err
	}
	resp, err := httpx.DoStream(ctx, "POST", agentBase(pc)+epChat, soloHeaders(acct), raw, p.cfg.ConnectTimeout())
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
		return classifyTrae(resp.Status, resp.Body)
	}
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		sink.Close()
		return classifyTrae(resp.Status, resp.Body)
	}
	return streamSOLO(resp.Stream, sink)
}

func classifyTrae(status int, body []byte) *provider.Error {
	e := provider.ClassifyBody(status, body)
	// Trae 业务码：1005 套餐次数用尽
	var probe struct {
		Code int64  `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Code == 1005 {
		return provider.Err(provider.KindQuota, status, "%s", probe.Msg)
	}
	return e
}

// streamSOLO 解析 SOLO 自定义 SSE 并输出到 sink（移植自 trae2api-web solosse.go）。
func streamSOLO(r io.Reader, sink provider.Sink) error {
	defer sink.Close()
	var (
		usage      *provider.Usage
		finish     string
		hasDelta   bool
		streamErr  *provider.Error
		sawDone    bool
	)
	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		switch ev.Event {
		case "output":
			var out struct {
				Response         string          `json:"response"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			}
			if json.Unmarshal([]byte(ev.Data), &out) != nil {
				return true
			}
			if out.Response != "" || out.ReasoningContent != "" {
				hasDelta = true
				sink.Delta(provider.Delta{Content: out.Response, Reasoning: out.ReasoningContent})
			}
			if len(out.ToolCalls) > 0 && string(out.ToolCalls) != "null" {
				var arr []map[string]any
				if err := json.Unmarshal(out.ToolCalls, &arr); err != nil {
					var one map[string]any
					if json.Unmarshal(out.ToolCalls, &one) == nil {
						arr = []map[string]any{one}
					}
				}
				for _, call := range arr {
					if call == nil {
						continue
					}
					idx := 0
					if v, ok := call["index"].(float64); ok {
						idx = int(v)
					}
					td := &provider.ToolDelta{Index: idx}
					if v, ok := call["id"].(string); ok {
						td.ID = v
					}
					fn, _ := call["function"].(map[string]any)
					if fn == nil {
						fn, _ = call["function_call"].(map[string]any)
					}
					if fn != nil {
						if n, ok := fn["name"].(string); ok {
							td.Name = n
						}
						if a, ok := fn["arguments"].(string); ok {
							td.Args = a
						}
					}
					hasDelta = true
					sink.Delta(provider.Delta{Tool: td})
				}
			}
		case "token_usage":
			var u map[string]any
			if json.Unmarshal([]byte(ev.Data), &u) == nil {
				pu := &provider.Usage{Present: true}
				pu.PromptTokens = numAsInt(u["prompt_tokens"])
				pu.CompletionTokens = numAsInt(u["completion_tokens"])
				pu.TotalTokens = numAsInt(u["total_tokens"])
				usage = pu
			}
		case "done":
			var d struct {
				FinishReason string `json:"finish_reason"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &d)
			finish = d.FinishReason
			sawDone = true
			return false
		case "error":
			var e struct {
				Code    float64 `json:"code"`
				Message string  `json:"message"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &e)
			streamErr = &provider.Error{Kind: provider.KindStream, Msg: fmt.Sprintf("code=%d %s", int64(e.Code), e.Message)}
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	if streamErr != nil {
		return streamErr
	}
	if finish == "" {
		finish = "stop"
		if !hasDelta && !sawDone {
			sink.StreamError(0, "上游返回空流")
		}
	}
	sink.Finish(provider.Finish{Reason: finish, Usage: usage})
	return nil
}

func numAsInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		var i int64
		fmt.Sscanf(n, "%d", &i)
		return i
	}
	return 0
}

// ── 登录 ──

// BuildLoginURL 构造 TRAE 授权页地址（复刻 login.sh 参数集）。
func BuildLoginURL(machineID, deviceID, callbackURL, consoleBase string) string {
	if consoleBase == "" {
		consoleBase = "https://www.trae.cn"
	}
	v := url.Values{}
	v.Set("login_version", "1")
	v.Set("auth_from", "solo")
	v.Set("login_channel", "native_ide")
	v.Set("plugin_version", "2.3.62834")
	v.Set("auth_type", "local")
	v.Set("client_id", ClientID)
	v.Set("redirect", "0")
	v.Set("login_trace_id", traceID(machineID, deviceID))
	v.Set("auth_callback_url", callbackURL)
	v.Set("machine_id", machineID)
	v.Set("device_id", deviceID)
	v.Set("x_device_id", deviceID)
	v.Set("x_machine_id", machineID)
	v.Set("x_device_brand", "PC")
	v.Set("x_device_type", "PC")
	v.Set("x_os_version", "1.0")
	v.Set("x_app_version", IdeVersion)
	v.Set("x_app_type", "stable")
	return consoleBase + "/authorization?" + v.Encode()
}

func traceID(machineID, deviceID string) string {
	h := machineID + deviceID
	if len(h) >= 16 {
		return h[len(h)-16:]
	}
	return strings.Repeat("0", 16-len(h)) + h
}

// CallbackInfo 解析授权回调链接的结果。
type CallbackInfo struct {
	RefreshToken string
	AccessToken  string // 无 refreshToken 时的兜底（userJwt.Token）
	UID          string
	Nickname     string
	EnterpriseID string
	ExpiresAt    int64
}

// ParseCallback 解析登录成功后浏览器跳转的回调 URL。
func ParseCallback(rawURL string) (*CallbackInfo, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("回调链接为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析回调链接失败: %w", err)
	}
	q := u.Query()
	info := &CallbackInfo{RefreshToken: q.Get("refreshToken")}

	userInfo := parseJSONParam(q.Get("userInfo"))
	info.UID = getStr(userInfo, "UserID")
	info.Nickname = getStr(userInfo, "ScreenName")
	info.EnterpriseID = getStr(userInfo, "TenantID")

	userJwt := parseJSONParam(q.Get("userJwt"))
	jwtToken := getStr(userJwt, "Token")
	jwtRefresh := getStr(userJwt, "RefreshToken")

	if info.RefreshToken == "" {
		info.RefreshToken = jwtRefresh
	}
	if info.RefreshToken == "" {
		info.AccessToken = jwtToken
		if jwtToken == "" {
			return nil, fmt.Errorf("回调链接缺少 refreshToken 与 userJwt.Token")
		}
		if exp := getInt64(userJwt, "TokenExpireAt"); exp > 0 {
			info.ExpiresAt = normalizeExpire(exp)
		}
	}
	return info, nil
}

// parseJSONParam 回调参数是 URL 编码的 JSON，可能被双重编码，容错解析。
func parseJSONParam(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	candidates := []string{raw}
	if uq, err := url.QueryUnescape(raw); err == nil && uq != raw {
		candidates = append(candidates, uq)
	}
	for _, c := range candidates {
		var obj map[string]any
		if json.Unmarshal([]byte(c), &obj) == nil && obj != nil {
			return obj
		}
	}
	return nil
}

func getStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getInt64(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case string:
		var i int64
		fmt.Sscanf(v, "%d", &i)
		return i
	}
	return 0
}

// ExchangeToken 用 refreshToken 换取访问 token（登录与刷新共用）。
func (p *Provider) ExchangeToken(ctx context.Context, refreshToken, apiHost string) (token, newRefresh string, expiresAt int64, err error) {
	pc := p.cfg.PC(store.PTrae)
	body := map[string]any{
		"ClientID":     ClientID,
		"RefreshToken": refreshToken,
		"ClientSecret": "-",
		"UserID":       "",
	}
	host := oauthBase(pc, apiHost)
	var out struct {
		Result struct {
			Token               string `json:"Token"`
			TokenExpireAt       int64  `json:"TokenExpireAt"`
			TokenExpireDuration int64  `json:"TokenExpireDuration"`
			RefreshToken        string `json:"RefreshToken"`
		} `json:"Result"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", host+epExchange, oauthHeaders(), body, &out, 30*time.Second); err != nil {
		return "", "", 0, err
	}
	if out.Result.Token == "" {
		return "", "", 0, fmt.Errorf("ExchangeToken 未返回 token")
	}
	exp := int64(0)
	if e := normalizeExpire(out.Result.TokenExpireAt); e > 0 {
		exp = e
	} else if out.Result.TokenExpireDuration > 0 {
		exp = time.Now().Unix() + out.Result.TokenExpireDuration
	}
	return out.Result.Token, out.Result.RefreshToken, exp, nil
}

// GetUserInfo 拉取账号信息（需 x-cloudide-token 头）。
func (p *Provider) GetUserInfo(ctx context.Context, token, apiHost string) (uid, nickname, enterpriseID string, err error) {
	h := oauthHeaders()
	h["x-cloudide-token"] = token
	body := map[string]any{"ReqSource": "IDE", "IDEVersion": IdeVersion}
	var raw map[string]any
	if _, err := httpx.DoJSON(ctx, "POST", oauthBase(p.cfg.PC(store.PTrae), apiHost)+epUserInfo, h, body, &raw, 30*time.Second); err != nil {
		return "", "", "", err
	}
	obj := raw
	if r, ok := raw["Result"].(map[string]any); ok {
		obj = r
	} else if r, ok := raw["result"].(map[string]any); ok {
		obj = r
	}
	uid = firstStr(obj, "UserID", "userId", "uid")
	nickname = firstStr(obj, "ScreenName", "screenName", "nickname")
	enterpriseID = firstStr(obj, "TenantID", "tenantId", "EnterpriseID", "enterpriseId")
	return uid, nickname, enterpriseID, nil
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := getStr(m, k); v != "" {
			return v
		}
	}
	return ""
}
