package gateway

import (
	"bytes"
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
	"tokenhub/internal/pj"
	"tokenhub/internal/pseudotools"
	"tokenhub/internal/pool"
	"tokenhub/internal/provider"
	"tokenhub/internal/shares"
	"tokenhub/internal/store"
	"tokenhub/internal/tokensaver"
	"tokenhub/internal/usagelog"
)

// Gateway OpenAI / Anthropic 兼容 API。
type Gateway struct {
	cfg    *config.Config
	pool   *pool.Pool
	shares *shares.Manager // 可为 nil（不启用分享钥匙）
	ulog   *usagelog.Log   // 可为 nil（不记录用量日志）

	// 破甲 auto 模式的按模型模板记忆（惰性加载 data/pj_auto.json）
	pjAuto struct {
		once  sync.Once
		picks *pjAutoMem
	}

	// auto 探测 singleflight：同一模型同一时刻只允许一个探测在跑
	pjProbeMu      sync.Mutex
	pjProbeRunning map[string]bool

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
	saved int64 // 网关层 token 优化节省的输入估算（Optimize 时写入）
}

func (u *usageRecorder) Delta(d provider.Delta)        { u.inner.Delta(d) }
func (u *usageRecorder) Close()                        { u.inner.Close() }
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
		e.SavedTokens = rec.saved
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

// optimizeReq 网关层 token 优化（确定性、无损语义）：JSON 紧凑化 +
// 会话内重复内容折叠。返回优化后的请求体；解析失败时原样返回。
func (g *Gateway) optimizeReq(model string, body []byte, rec *usageRecorder) []byte {
	g.dumpReq(model, body)
	if !g.cfg.OptDedupOn() && !g.cfg.OptCompactJSONOn() {
		return body
	}
	out, res := tokensaver.Optimize(body, tokensaver.Options{
		Dedup:       g.cfg.OptDedupOn(),
		CompactJSON: g.cfg.OptCompactJSONOn(),
		NearDup:     g.cfg.OptNearDupOn(),
		MinChars:    500,
	})
	if rec != nil {
		rec.saved = res.SavedTokens
	}
	if res.SavedTokens > 0 {
		logx.Infof("gateway", "token 优化(%s): 紧凑 JSON %d / 重复折叠 %d / 过期结果 %d / 近重复 %d，省约 %d tokens",
			model, res.Compacted, res.Deduped, res.Staled+res.ImagesDedup, res.NearDuped, res.SavedTokens)
	}
	return out
}

// pjNextStrategy failover 用：给定当前策略，返回重试用的策略。
// 策略表按「强→弱」排序：当前策略被拒说明它不够强，直接上表头（最强组合）；
// 当前已是最强（表头）时返回 ""——没有更强的了，不再重试。
func pjNextStrategy(cur string) string {
	all := pj.Strategies()
	if len(all) == 0 {
		return ""
	}
	for i, s := range all {
		if s == cur {
			if i == 0 {
				return "" // 已是最强
			}
			return all[0]
		}
	}
	return all[0] // 未知/为空（auto 未展开）→ 直接上最强
}

// shouldPJRetry 非流式回复是否需要破甲重试：破甲开着、回复被判拒答或空。
// 空回复（上游吐了 200 但没有正文，模型把整段内容吞掉时常见）同样重试——
// 重试换更强的策略，大概率能拿到实质内容；最差也就是再空一次。
func (g *Gateway) shouldPJRetry(provName string, text string) bool {
	if g.cfg == nil || !g.cfg.PJOn() {
		return false
	}
	if g.cfg.PJScope == config.PJScopeOllama && provName != store.POllama {
		return false
	}
	if strings.TrimSpace(text) == "" {
		return true
	}
	return pj.LooksRefused(text)
}

// applyPJ 破甲：把设置页选中的策略（模板 / 前置段 / 组合 / auto）套到请求上。
// 对所有提供商生效（cfg.PJScope=all 时是默认行为）——这是「同一个入口，对所有
// Provider 都走一遍」的那一层：无论请求路由到 workbuddy / trae / zcode / qwen /
// ollama，都在发给上游之前过这里。总开关关闭时完全不动请求。
func (g *Gateway) applyPJ(provName string, model string, body []byte) []byte {
	return g.applyPJModel(provName, model, "", body)
}

// applyPJModel 同上，但带上「最原始的请求模型名」：pool 可能把裸模型名改写成
// 别的形状，auto 记忆要用客户端给的那个名字做 key，才能跨账号/跨前缀稳定命中。
// 注入管线（顺序固定）：
//  1. 查策略（auto 按记忆展开）；
//  2. 折叠客户端回传历史里重复出现的破甲段（PJCompact，默认开）；
//  3. 按注入预算选形状（完整 → 精简 → 只留最强前置段 → 放弃）；
//  4. 落注入（ApplyPlan），日志带策略、档位与估算 token 成本。
func (g *Gateway) applyPJModel(provName string, model string, rawModel string, body []byte) []byte {
	return g.applyPJModelOpts(provName, model, rawModel, body, false)
}

// applyPJModelOpts 注入管线核心。frontOnly=true 时（带 tools 的 agent 请求）：
//  1. 先把客户端回传历史里已套壳的模板段摘干净（StripTemplate——模板在工具流里
//     会污染 tool_calls 生成，也常触发「我不能按 BetterDAN 那种格式回答」的元评论拒答）；
//  2. 只挂前置段（模板砍掉），保住作用域约束又不碰最后一轮正文。
func (g *Gateway) applyPJModelOpts(provName string, model string, rawModel string, body []byte, frontOnly bool) []byte {
	if g.cfg == nil || !g.cfg.PJOn() {
		return body
	}
	// 生效范围：默认全部提供商；显式配置成 ollama 时退回旧行为（只打本机）
	if g.cfg.PJScope == config.PJScopeOllama && provName != store.POllama {
		return body
	}
	strategy := g.cfg.PJMode
	if strategy == "auto" {
		key := rawModel
		if key == "" {
			key = model
		}
		return g.pjAutoApplyOpts(key, body, frontOnly)
	}
	// 折叠历史里重复的破甲段（客户端把带段上下文整包回传的场景）
	body = g.foldPJDups(body)
	if frontOnly {
		if out, changed := pj.StripTemplate(body); changed {
			logx.Infof("gateway", "破甲：请求带 tools，已摘除历史里的模板段（策略改用前置段 %s）", strategy)
			body = out
		}
		plan := pj.PlanForBudget(strategy, g.pjEffectiveBudget())
		plan.Tpl = "" // 前置段 only
		out := pj.ApplyPlan(plan, body)
		if !bytes.Equal(out, body) {
			logx.Infof("gateway", "破甲已注入（%s / %s，策略 %s，%s·tools）≈+%d tokens",
				provName, model, strategy, pjShapeLabel(plan), pjInjectionCost(strategy, plan))
		}
		return out
	}
	plan := pj.PlanForBudget(strategy, g.pjEffectiveBudget())
	out := pj.ApplyPlan(plan, body)
	if !bytes.Equal(out, body) {
		logx.Infof("gateway", "破甲已注入（%s / %s，策略 %s，%s）≈+%d tokens",
			provName, model, strategy, pjShapeLabel(plan), pjInjectionCost(strategy, plan))
	} else if !plan.Has {
		logx.Infof("gateway", "破甲未注入（%s / %s，策略 %s 超出预算 %d 字符且精简档仍装不下）",
			provName, model, strategy, g.cfg.PJBudget)
	}
	return out
}

// requestHasTools 判断 OpenAI 形状的请求是否携带工具定义。
func requestHasTools(body []byte) bool {
	var peek struct {
		Tools []any `json:"tools"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return false
	}
	return len(peek.Tools) > 0
}

// pseudoProviders 没有原生工具能力的提供商（免费池模型），客户端带工具时
// 自动改走伪工具协议（提示词注入 + 标记解析还原）。
var pseudoProviders = map[string]bool{store.PFree: true, store.POpencode: true, store.PDeepSeek: true}

// engagePseudoTools 判定并改写：返回（改写后 body, 是否启用伪工具）。
func (g *Gateway) engagePseudoTools(provName string, body []byte) ([]byte, bool) {
	if !pseudoProviders[provName] || !requestHasTools(body) {
		return body, false
	}
	return pseudotools.Engage(body), true
}

// pjEffectiveBudget 注入预算：PJLean 开启时把预算压到「完整版装不下、精简版装得下」
// 的区间（精简 armor_max 约 916 字符 + 模板余量 → 1400 上限最稳），
// PlanForBudget 自然落精简档；关闭时按原预算走完整档。
func (g *Gateway) pjEffectiveBudget() int {
	if g.cfg.PJLean {
		const leanCap = 1400 // 完整 armor_max≈2535 字符装不下；精简 916 + aim 模板可过
		if g.cfg.PJBudget > leanCap {
			return leanCap
		}
	}
	return g.cfg.PJBudget
}

// FrontLeanOpts 按配置档位生成前置段注入选项（failover 重打时保持同形状）。
func FrontLeanOpts(g *Gateway) pj.FrontOpts {
	return pj.FrontOpts{Lean: g.cfg.PJLean}
}

// foldPJDups 折叠请求里重复出现的破甲前置段（PJCompact 开关，默认开）。
// 只开 tokensaver 的 Pass 0（DupSystem），其余优化归 optimizeReq 管。
func (g *Gateway) foldPJDups(body []byte) []byte {
	if !g.cfg.PJCompactOn() {
		return body
	}
	out, res := tokensaver.Optimize(body, tokensaver.Options{Dedup: true, DupSystem: true, MinChars: 500})
	if res.DupSysFolded > 0 {
		logx.Infof("gateway", "破甲段去重：折叠 %d 条重复 system 段，省约 %d tokens", res.DupSysFolded, res.SavedTokens)
	}
	return out
}

// pjShapeLabel 注入档位标签（日志用）。
func pjShapeLabel(plan pj.BudgetPlan) string {
	switch {
	case plan.Has && plan.Lean && plan.Front != "":
		return "精简档"
	case plan.Has && plan.Front == "":
		return "仅模板"
	case plan.Has:
		return "完整档"
	default:
		return "放弃"
	}
}

// pjInjectionCost 估算本次注入的 token 成本（前置段 + 模板，按所选形状）。
func pjInjectionCost(strategy string, plan pj.BudgetPlan) int64 {
	var cost int64
	if plan.Has {
		if f := pj.GetFront(plan.Front); f != nil {
			s := f.Prompt
			if plan.Lean && f.Lean != "" {
				s = f.Lean
			}
			cost += tokensaver.FrontTokenCost(s)
		}
		for _, p := range strings.Split(strategy, "+") {
			if t := pj.Get(strings.TrimSpace(p)); t != nil && !plan.Lean {
				cost += tokensaver.FrontTokenCost(t.Prompt)
			}
		}
	}
	return cost
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
		body = g.optimizeReq(model, body, rec)
		body, pseudo := g.engagePseudoTools(prov.Name(), body)
		prePJ := body // 流式门重试底稿：优化后、破甲注入前
		body = g.applyPJModelOpts(prov.Name(), bare, model, body, requestHasTools(body))
		err := g.gateChat(ctx, prov.Name(), model, model, body, prePJ, sink, rec, pseudo)
		if shareID != "" {
			g.shares.Record(shareID, prov.Name(), rec.tokens())
		}
		g.logUsage(r, model, prov.Name(), start, rec, err)
		g.notifyAfterCall(prov.Name(), err)
		if err != nil {
			logx.Errorf("gateway", "chat(%s) 失败: %v", model, err)
			// gateChat 失败时没向真 sink 写过字节，回退为标准 HTTP 错误
			if !sink.Wrote() {
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
	body = g.optimizeReq(model, body, rec)
	body, pseudo := g.engagePseudoTools(prov.Name(), body)
	prePJ := body // failover 重试底稿：优化后、破甲注入前
	body = g.applyPJModelOpts(prov.Name(), bare, model, body, requestHasTools(body))
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
	// 伪工具解析先于破甲判定：思考流里也可能藏着 <tool_call>（ERNIE 惯犯），
	// 解析出的调用落进 raw 的 tool_calls，有调用就不再触发拒答重试。
	if pseudo {
		raw = pseudotools.ApplyToResponse(raw)
	}
	// 破甲 failover：回复被判拒答且还有更强策略时，用最强策略在底稿上重打一次（非流式最多一次）。
	// 计入同一份 share 用量与 usage 记录，避免重复计费；重试结果更好才替换。
	// 带 tools 的重试同样走前置段 only（模板在工具流里会污染 tool_calls）。
	toolsReq := requestHasTools(prePJ)
	if !pseudotools.HasToolCalls(raw) {
		if txt := openaiContentText(raw); g.shouldPJRetry(prov.Name(), txt) {
			if stronger := pjNextStrategy(g.pjEffectiveStrategy(model)); stronger != "" {
				logx.Infof("gateway", "破甲 failover：chat(%s) 回复被判拒答/空，换策略 %s 重试", model, stronger)
				retryBody := pj.ApplyStrategy(stronger, prePJ)
				if toolsReq {
					if out, changed := pj.StripTemplate(retryBody); changed {
						retryBody = out
					}
					if plan := pj.PlanForBudget(stronger, g.pjEffectiveBudget()); plan.Front != "" {
						retryBody = pj.ApplyFrontOpts(plan.Front, retryBody, FrontLeanOpts(g))
					}
				}
				retrySink := NewAggregateSink(outModel)
				retryRec := &usageRecorder{inner: retrySink}
				if err2 := g.pool.Chat(ctx, model, retryBody, retryRec); err2 == nil {
					if raw2, perr2 := retrySink.Result(); perr2 == nil {
						g.logUsage(r, model, prov.Name(), start, retryRec, nil)
						if pseudo {
							raw2 = pseudotools.ApplyToResponse(raw2)
						}
						txt2 := openaiContentText(raw2)
						if !pseudotools.HasToolCalls(raw2) && txt2 != "" && pj.LooksRefused(txt2) {
							logx.Infof("gateway", "破甲 failover：重试仍被拒，返回原结果")
						} else {
							raw = raw2
							logx.Infof("gateway", "破甲 failover：重试成功（策略 %s）", stronger)
						}
					}
				} else {
					logx.Infof("gateway", "破甲 failover：重试调用失败，返回原结果: %v", err2)
				}
			}
		}
	}
	if pseudo {
		raw = pseudotools.ApplyToResponse(raw)
	}
	if out, stripped := pseudotools.StripDSML(raw); stripped {
		logx.Infof("gateway", "已剥离响应中的 DSML 泄漏标记")
		raw = out
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
	logx.Infof("gateway", "chat(%s) 完成 %.1fs", model, time.Since(start).Seconds())
}

// pjEffectiveStrategy 当前请求实际会用的破甲策略（auto 按模型记忆展开；拿不到记忆时给 ""，
// pjNextStrategy 会把 "" 当未知策略直接上最强）。
func (g *Gateway) pjEffectiveStrategy(rawModel string) string {
	if g.cfg == nil {
		return ""
	}
	if g.cfg.PJMode == "auto" {
		if s, ok := g.pjAutoPick(rawModel); ok {
			return s
		}
		return ""
	}
	return g.cfg.PJMode
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
		openaiReq = g.optimizeReq(model, openaiReq, rec)
		openaiReq, pseudo := g.engagePseudoTools(prov.Name(), openaiReq)
		prePJ := openaiReq // 流式门重试底稿：优化后、破甲注入前
		openaiReq = g.applyPJModelOpts(prov.Name(), bare, model, openaiReq, requestHasTools(openaiReq))
		err := g.gateChatAnthropic(ctx, prov.Name(), model, model, openaiReq, prePJ, sink, rec, pseudo)
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
	openaiReq = g.optimizeReq(model, openaiReq, rec)
	openaiReq, pseudo := g.engagePseudoTools(prov.Name(), openaiReq)
	prePJ := openaiReq // failover 重试底稿：优化后、破甲注入前
	openaiReq = g.applyPJModel(prov.Name(), bare, model, openaiReq)
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
	// 伪工具解析先于破甲判定：思考块里的 <tool_call> 也要还原成 tool_use。
	if pseudo {
		raw = pseudotools.ApplyToAnthropicResponse(raw)
	}
	// 破甲 failover（Anthropic 形状）：拒答时用最强策略在底稿上重打一次，结果更好才替换
	// 带 tools 的重试同样走前置段 only。
	toolsReq := requestHasTools(prePJ)
	if !anthropicHasToolUse(raw) {
		if txt := anthropicRespText(raw); g.shouldPJRetry(prov.Name(), txt) {
			if stronger := pjNextStrategy(g.pjEffectiveStrategy(model)); stronger != "" {
				logx.Infof("gateway", "破甲 failover：messages(%s) 回复被判拒答/空，换策略 %s 重试", model, stronger)
				retryBody := pj.ApplyStrategy(stronger, prePJ)
				if toolsReq {
					if out, changed := pj.StripTemplate(retryBody); changed {
						retryBody = out
					}
					if plan := pj.PlanForBudget(stronger, g.pjEffectiveBudget()); plan.Front != "" {
						retryBody = pj.ApplyFrontOpts(plan.Front, retryBody, FrontLeanOpts(g))
					}
				}
				retrySink := NewAnthropicAggregateSink(outModel)
				retryRec := &usageRecorder{inner: retrySink}
				if err2 := g.pool.Chat(ctx, model, retryBody, retryRec); err2 == nil {
					if raw2, perr2 := retrySink.Result(); perr2 == nil {
						g.logUsage(r, model, prov.Name(), start, retryRec, nil)
						if pseudo {
							raw2 = pseudotools.ApplyToAnthropicResponse(raw2)
						}
						txt2 := anthropicRespText(raw2)
						if !anthropicHasToolUse(raw2) && txt2 != "" && pj.LooksRefused(txt2) {
							logx.Infof("gateway", "破甲 failover：重试仍被拒，返回原结果")
						} else {
							raw = raw2
							logx.Infof("gateway", "破甲 failover：重试成功（策略 %s）", stronger)
						}
					}
				} else {
					logx.Infof("gateway", "破甲 failover：重试调用失败，返回原结果: %v", err2)
				}
			}
		}
	}
	if out, stripped := pseudotools.StripDSML(raw); stripped {
		logx.Infof("gateway", "已剥离响应中的 DSML 泄漏标记")
		raw = out
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(raw)
	logx.Infof("gateway", "messages(%s) 完成 %.1fs", model, time.Since(start).Seconds())
}

// anthropicHasToolUse 判断 Anthropic 非流式响应里是否已带 tool_use 块。
func anthropicHasToolUse(raw []byte) bool {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return false
	}
	for _, b := range resp.Content {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}

// anthropicRespText 从 Anthropic 非流式响应 JSON 里取全部 text 块拼成文本。
func anthropicRespText(raw []byte) string {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range resp.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
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
