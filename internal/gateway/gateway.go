package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/logx"
	"tokenhub/internal/pool"
	"tokenhub/internal/provider"
	"tokenhub/internal/shares"
	"tokenhub/internal/usagelog"
)

// Gateway OpenAI / Anthropic 兼容 API。
type Gateway struct {
	cfg    *config.Config
	pool   *pool.Pool
	shares *shares.Manager // 可为 nil（不启用分享钥匙）
	ulog   *usagelog.Log   // 可为 nil（不记录用量日志）

	// AfterCall 每次 API 调用完成后异步回调（参数：提供商名、成功与否），
	// 用于触发额度快照自动刷新（可为 nil）。
	AfterCall func(provName string, ok bool)
}

func New(cfg *config.Config, pl *pool.Pool, sh *shares.Manager, ul *usagelog.Log) *Gateway {
	return &Gateway{cfg: cfg, pool: pl, shares: sh, ulog: ul}
}

type ctxKey int

const (
	ctxKeyShareID ctxKey = iota
	ctxKeyShareName
)

// shareIDFromCtx 取当前请求的分享钥匙 ID（主人调用返回空）。
func shareIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyShareID).(string)
	return v
}

// shareNameFromCtx 取当前请求的分享钥匙名称（主人调用返回空）。
func shareNameFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyShareName).(string)
	return v
}

func withShareCtx(r *http.Request, id, name string) context.Context {
	ctx := context.WithValue(r.Context(), ctxKeyShareID, id)
	return context.WithValue(ctx, ctxKeyShareName, name)
}

// Register 挂载到 mux。
func (g *Gateway) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", g.withAuth(g.openaiChat))
	mux.HandleFunc("POST /v1/completions", g.withAuth(g.openaiChat))
	mux.HandleFunc("GET /v1/models", g.withAuth(g.models))
	mux.HandleFunc("POST /v1/messages", g.withAuth(g.anthropicMessages))
	mux.HandleFunc("POST /v1/messages/count_tokens", g.withAuth(g.countTokens))
	mux.HandleFunc("GET /status", g.withAuth(g.ownerOnly(g.status)))
	mux.HandleFunc("GET /healthz", g.health)
}

// ── 中间件 ──

func (g *Gateway) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key")
			w.WriteHeader(204)
			return
		}
		if !g.authorize(r, w) {
			return
		}
		next(w, r)
	}
}

// authorize 主 Key 或分享钥匙校验，通过返回 true。
func (g *Gateway) authorize(r *http.Request, w http.ResponseWriter) bool {
	key := bearerKey(r)
	if key == "" {
		writeOpenAIError(w, 401, "invalid_api_key", "缺少 API key。请求头需携带 Authorization: Bearer <key>，或到面板查看正确的 key。")
		return false
	}
	if key == g.cfg.APIKey {
		return true // 主人调用
	}
	if g.shares == nil {
		writeOpenAIError(w, 401, "invalid_api_key", "API key 无效。")
		return false
	}
	if s, ok := g.shares.Resolve(key); ok {
		if !s.Enabled {
			writeOpenAIError(w, 403, "share_disabled", "该分享钥匙已被停用。")
			return false
		}
		*r = *r.WithContext(withShareCtx(r, s.ID, s.Name))
		return true
	}
	writeOpenAIError(w, 401, "invalid_api_key", "API key 无效。请在请求头携带 Authorization: Bearer <TokenHub 的 apiKey>，或到面板查看正确的 key。")
	return false
}

func bearerKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

// ownerOnly 仅主人（主 Key）可访问；分享钥匙请求返回 403。
func (g *Gateway) ownerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if shareIDFromCtx(r) != "" {
			writeOpenAIError(w, 403, "forbidden", "该端点不对分享钥匙开放。")
			return
		}
		next(w, r)
	}
}

// usageRecorder 包装 Sink，抓取上游返回的 token 用量（供分享钥匙计量）。
type usageRecorder struct {
	inner provider.Sink
	mu    sync.Mutex
	usage *provider.Usage
}

func (u *usageRecorder) Delta(d provider.Delta)  { u.inner.Delta(d) }
func (u *usageRecorder) Close()                  { u.inner.Close() }
func (u *usageRecorder) StreamError(c int64, m string) { u.inner.StreamError(c, m) }

func (u *usageRecorder) Finish(f provider.Finish) {
	u.mu.Lock()
	if f.Usage != nil {
		u.usage = f.Usage
	}
	u.mu.Unlock()
	u.inner.Finish(f)
}

func (u *usageRecorder) tokens() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.usage == nil || !u.usage.Present {
		return 0
	}
	return u.usage.BilledTokens()
}

// pc 返回 (prompt, completion, cacheRead, cacheWrite, total)，无 usage 时全 0。
func (u *usageRecorder) pc() (int64, int64, int64, int64, int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.usage == nil || !u.usage.Present {
		return 0, 0, 0, 0, 0
	}
	return u.usage.PromptTokens, u.usage.CompletionTokens, u.usage.CacheReadTokens, u.usage.CacheWriteTokens, u.usage.BilledTokens()
}

// logUsage 追加一条用量日志（ulog 未启用时忽略）。
func (g *Gateway) logUsage(r *http.Request, model, provName string, start time.Time, rec *usageRecorder, err error) {
	if g.ulog == nil {
		return
	}
	e := usagelog.Entry{
		Time:       time.Now().Unix(),
		Caller:     shareNameFromCtx(r),
		Provider:   provName,
		Model:      model,
		DurationMs: time.Since(start).Milliseconds(),
		Ok:         err == nil,
	}
	if err != nil {
		e.ErrMsg = asPerr(err).Msg
	}
	if rec != nil {
		e.PromptTokens, e.CompletionTokens, e.CacheReadTokens, e.CacheWriteTokens, e.TotalTokens = rec.pc()
	}
	g.ulog.Add(e)
}

// notifyAfterCall 统一触发调用后回调（异步、防抖由接收方负责）。
func (g *Gateway) notifyAfterCall(provName string, err error) {
	if g.AfterCall == nil {
		return
	}
	go g.AfterCall(provName, err == nil)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": code, "code": code},
	})
}

func writeAnthropicError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	})
}

// mapClientStatus 把提供商错误映射为对外状态码。
func mapClientStatus(pe *provider.Error) int {
	switch pe.Kind {
	case provider.KindAuth, provider.KindQuota:
		return 502 // 账号侧问题，对客户端表现为上游错误
	case provider.KindRate:
		return 429
	case provider.KindClient:
		if pe.Status >= 400 && pe.Status < 500 {
			return pe.Status
		}
		return 400
	case provider.KindModel:
		return 404
	case provider.KindServer:
		if pe.Status >= 400 {
			return pe.Status
		}
		return 502
	default:
		if pe.Status >= 400 {
			return pe.Status
		}
		return 502
	}
}

// ── OpenAI ──

func (g *Gateway) openaiChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "读取请求体失败: "+err.Error())
		return
	}
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek)
	model := peek.Model
	prov, bare := g.pool.ResolveModel(model)
	if prov == nil {
		writeOpenAIError(w, 503, "no_provider", "没有可用提供商")
		return
	}
	shareID := shareIDFromCtx(r)
	if shareID != "" {
		checkModel := bare
		if checkModel == "" {
			checkModel = model
		}
		if msg := g.shares.Authorize(shareID, prov.Name(), checkModel); msg != "" {
			writeOpenAIError(w, 429, "share_limit", msg)
			return
		}
	}
	outModel := bare
	if outModel == "" {
		outModel = model
	}
	ctx := r.Context()
	start := time.Now()
	if peek.Stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		fl, _ := w.(http.Flusher)
		sink := NewOpenAIStreamSink(w, fl, outModel)
		rec := &usageRecorder{inner: sink}
		err := g.pool.Chat(ctx, model, body, rec)
		if shareID != "" {
			g.shares.Record(shareID, prov.Name(), rec.tokens())
		}
		g.logUsage(r, model, prov.Name(), start, rec, err)
		g.notifyAfterCall(prov.Name(), err)
		if err != nil {
			logx.Errorf("gateway", "chat(%s) 失败: %v", model, err)
			if !sink.Wrote() {
				// 还没写出任何内容：回退为标准 HTTP 错误
				pe := asPerr(err)
				writeOpenAIError(w, mapClientStatus(pe), string(pe.Kind), pe.Msg)
				return
			}
		}
		logx.Infof("gateway", "chat(%s) stream 完成 %.1fs", model, time.Since(start).Seconds())
		return
	}
	sink := NewAggregateSink(outModel)
	rec := &usageRecorder{inner: sink}
	err = g.pool.Chat(ctx, model, body, rec)
	if shareID != "" {
		g.shares.Record(shareID, prov.Name(), rec.tokens())
	}
	g.logUsage(r, model, prov.Name(), start, rec, err)
	g.notifyAfterCall(prov.Name(), err)
	if err != nil {
		pe := asPerr(err)
		logx.Errorf("gateway", "chat(%s) 失败: %v", model, err)
		writeOpenAIError(w, mapClientStatus(pe), string(pe.Kind), pe.Msg)
		return
	}
	raw, perr := sink.Result()
	if perr != nil {
		writeOpenAIError(w, mapClientStatus(perr), string(perr.Kind), perr.Msg)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
	logx.Infof("gateway", "chat(%s) 完成 %.1fs", model, time.Since(start).Seconds())
}

func asPerr(err error) *provider.Error {
	var pe *provider.Error
	if ok := asError(err, &pe); ok {
		return pe
	}
	return provider.Err(provider.KindServer, 502, "%v", err)
}

func asError(err error, target **provider.Error) bool {
	if pe, ok := err.(*provider.Error); ok {
		*target = pe
		return true
	}
	return false
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	all := g.pool.AllModels(r.Context())
	var data []map[string]any
	seen := map[string]bool{}
	addModel := func(id string, owner string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		data = append(data, map[string]any{"id": id, "object": "model", "owned_by": owner})
	}
	// 路由顺序：默认提供商优先
	order := []string{g.cfg.DefaultProvider}
	for _, name := range config.AllProviders() {
		if name != g.cfg.DefaultProvider {
			order = append(order, name)
		}
	}
	for _, name := range order {
		for _, m := range all[name] {
			addModel(m.ID, name)
			if g.cfg.ExposePrefixModels {
				addModel(name+":"+m.ID, name)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (g *Gateway) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service":  config.AppName,
		"version":  config.AppVersion,
		"accounts": g.pool.Status(),
	})
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// ── Anthropic ──

// anthropicToOpenAI 把 Anthropic messages 请求转成 OpenAI chat 请求。
func anthropicToOpenAI(src map[string]any) map[string]any {
	out := map[string]any{"model": src["model"], "stream": src["stream"]}
	if v, ok := src["max_tokens"].(float64); ok {
		out["max_tokens"] = v
	}
	if v, ok := src["temperature"].(float64); ok {
		out["temperature"] = v
	}
	if v, ok := src["top_p"].(float64); ok {
		out["top_p"] = v
	}
	if ss, ok := src["stop_sequences"].([]any); ok {
		out["stop"] = ss
	}
	var messages []any
	if sys := src["system"]; sys != nil {
		messages = append(messages, map[string]any{"role": "system", "content": systemText(sys)})
	}
	if msgs, ok := src["messages"].([]any); ok {
		for _, mv := range msgs {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			switch role {
			case "user":
				messages = append(messages, map[string]any{"role": "user", "content": blocksToOpenAI(m["content"], true)})
			case "assistant":
				om := map[string]any{"role": "assistant", "content": blocksToOpenAI(m["content"], false)}
				var toolCalls []any
				if blocks, ok := m["content"].([]any); ok {
					for _, bv := range blocks {
						b, ok := bv.(map[string]any)
						if !ok || b["type"] != "tool_use" {
							continue
						}
						inputJSON, _ := json.Marshal(b["input"])
						toolCalls = append(toolCalls, map[string]any{
							"id":   b["id"],
							"type": "function",
							"function": map[string]any{
								"name":      b["name"],
								"arguments": string(inputJSON),
							},
						})
					}
				}
				if len(toolCalls) > 0 {
					om["tool_calls"] = toolCalls
					if om["content"] == "" {
						om["content"] = nil
					}
				}
				messages = append(messages, om)
			}
		}
	}
	out["messages"] = messages
	if tools, ok := src["tools"].([]any); ok && len(tools) > 0 {
		var otools []any
		for _, tv := range tools {
			t, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			otools = append(otools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t["name"],
					"description": t["description"],
					"parameters":  t["input_schema"],
				},
			})
		}
		if len(otools) > 0 {
			out["tools"] = otools
		}
	}
	if tc, ok := src["tool_choice"].(map[string]any); ok {
		typ, _ := tc["type"].(string)
		switch typ {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "tool":
			if name, _ := tc["name"].(string); name != "" {
				out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": name}}
			}
		}
	}
	return out
}

func systemText(sys any) string {
	switch s := sys.(type) {
	case string:
		return s
	case []any:
		var sb strings.Builder
		for _, bv := range s {
			if b, ok := bv.(map[string]any); ok {
				if t, ok := b["text"].(string); ok {
					sb.WriteString(t)
					sb.WriteString("\n")
				}
			}
		}
		return sb.String()
	}
	return ""
}

// blocksToOpenAI Anthropic content（string 或 blocks）→ OpenAI content。
func blocksToOpenAI(content any, isUser bool) any {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var out []any
		for _, bv := range c {
			b, ok := bv.(map[string]any)
			if !ok {
				continue
			}
			switch b["type"] {
			case "text":
				if t, _ := b["text"].(string); t != "" {
					out = append(out, map[string]any{"type": "text", "text": t})
				}
			case "image":
				if src, ok := b["source"].(map[string]any); ok {
					if src["type"] == "base64" {
						media, _ := src["media_type"].(string)
						data, _ := src["data"].(string)
						out = append(out, map[string]any{
							"type":      "image_url",
							"image_url": map[string]any{"url": fmt.Sprintf("data:%s;base64,%s", media, data)},
						})
					}
				}
			case "tool_result":
				// tool_result → OpenAI tool 消息
				text := ""
				switch rc := b["content"].(type) {
				case string:
					text = rc
				case []any:
					var sb strings.Builder
					for _, rv := range rc {
						if rb, ok := rv.(map[string]any); ok {
							if t, ok := rb["text"].(string); ok {
								sb.WriteString(t)
							}
						}
					}
					text = sb.String()
				}
				tid, _ := b["tool_use_id"].(string)
				out = append(out, map[string]any{
					"__tool_result": true,
					"tool_call_id":  tid,
					"content":       text,
				})
			}
		}
		// 把 tool_result 拆成独立 tool 消息
		var final []any
		for _, item := range out {
			if m, ok := item.(map[string]any); ok && m["__tool_result"] == true {
				delete(m, "__tool_result")
				final = append(final, map[string]any{"role": "tool", "tool_call_id": m["tool_call_id"], "content": m["content"]})
			} else {
				final = append(final, item)
			}
		}
		if len(final) == 1 {
			if m, ok := final[0].(map[string]any); !ok || m["role"] != "tool" {
				if arr, ok := final[0].([]any); ok {
					return arr
				}
			}
		}
		if len(final) == 1 {
			// 单条 text → 纯字符串
			if m, ok := final[0].(map[string]any); ok && m["type"] == "text" {
				return m["text"]
			}
		}
		return final
	}
	return ""
}

func (g *Gateway) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", "读取请求体失败: "+err.Error())
		return
	}
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", "请求不是合法 JSON")
		return
	}
	model, _ := src["model"].(string)
	stream, _ := src["stream"].(bool)
	prov, bare := g.pool.ResolveModel(model)
	if prov == nil {
		writeAnthropicError(w, 503, "api_error", "没有可用提供商")
		return
	}
	shareID := shareIDFromCtx(r)
	if shareID != "" {
		checkModel := bare
		if checkModel == "" {
			checkModel = model
		}
		if msg := g.shares.Authorize(shareID, prov.Name(), checkModel); msg != "" {
			writeAnthropicError(w, 429, "rate_limit_error", msg)
			return
		}
	}
	outModel := bare
	if outModel == "" {
		outModel = model
	}
	openaiReq, err := json.Marshal(anthropicToOpenAI(src))
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", err.Error())
		return
	}
	ctx := r.Context()
	start := time.Now()
	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		fl, _ := w.(http.Flusher)
		sink := NewAnthropicStreamSink(w, fl, outModel)
		rec := &usageRecorder{inner: sink}
		err := g.pool.Chat(ctx, model, openaiReq, rec)
		if shareID != "" {
			g.shares.Record(shareID, prov.Name(), rec.tokens())
		}
		g.logUsage(r, model, prov.Name(), start, rec, err)
		g.notifyAfterCall(prov.Name(), err)
		if err != nil {
			logx.Errorf("gateway", "messages(%s) 失败: %v", model, err)
			if !sink.Wrote() {
				pe := asPerr(err)
				writeAnthropicError(w, mapClientStatus(pe), "api_error", pe.Msg)
				return
			}
		}
		// 注意：Record 只能调用一次，重复调用会把分享钥匙用量翻倍
		logx.Infof("gateway", "messages(%s) stream 完成 %.1fs", model, time.Since(start).Seconds())
		return
	}
	sink := NewAnthropicAggregateSink(outModel)
	rec := &usageRecorder{inner: sink}
	if err := g.pool.Chat(ctx, model, openaiReq, rec); err != nil {
		if shareID != "" {
			g.shares.Record(shareID, prov.Name(), rec.tokens())
		}
		g.logUsage(r, model, prov.Name(), start, rec, err)
		g.notifyAfterCall(prov.Name(), err)
		pe := asPerr(err)
		writeAnthropicError(w, mapClientStatus(pe), "api_error", pe.Msg)
		return
	}
	if shareID != "" {
		g.shares.Record(shareID, prov.Name(), rec.tokens())
	}
	g.logUsage(r, model, prov.Name(), start, rec, nil)
	g.notifyAfterCall(prov.Name(), nil)
	raw, perr := sink.Result()
	if perr != nil {
		writeAnthropicError(w, mapClientStatus(perr), "api_error", perr.Msg)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(raw)
	logx.Infof("gateway", "messages(%s) 完成 %.1fs", model, time.Since(start).Seconds())
}

func (g *Gateway) countTokens(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	var src map[string]any
	_ = json.Unmarshal(body, &src)
	text := systemText(src["system"])
	if msgs, ok := src["messages"].([]any); ok {
		for _, mv := range msgs {
			text += blocksToOpenAIString(mv)
		}
	}
	// CJK 粗略估算：字节数 / 3
	est := len([]rune(text))/2 + len(text)/6
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": est})
}

func blocksToOpenAIString(mv any) string {
	m, ok := mv.(map[string]any)
	if !ok {
		return ""
	}
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, bv := range c {
			if b, ok := bv.(map[string]any); ok {
				if t, ok := b["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// LocalIP 供面板展示局域网地址。
func LocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
