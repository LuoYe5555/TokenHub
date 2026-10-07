// Package web 内嵌 Web 面板：账号管理、登录流、额度、日志、配置。
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/autoslide"
	"tokenhub/internal/logx"
	"tokenhub/internal/pool"
	"tokenhub/internal/provider"
	"tokenhub/internal/provider/zcode"
	"tokenhub/internal/scheduler"
	"tokenhub/internal/traemigrate"
	"tokenhub/internal/traeswitch"
	"tokenhub/internal/usagelog"
	"tokenhub/internal/shares"
	"tokenhub/internal/store"
	"tokenhub/internal/tunnel"
)

// Panel 管理面板。
type Panel struct {
	cfg    *config.Config
	store  *store.Store
	pl     *pool.Pool
	sched  *scheduler.Scheduler
	shares *shares.Manager
	tunnel *tunnel.Manager
	ulog   *usagelog.Log

	mu          sync.Mutex
	wbStates    map[string]string            // state → realm
	zcodeSess   map[string]*zcode.LoginSession // flowID → session
	trae        map[string]*traePending        // pendingID → pending
	traeCBStarted bool

	wvDispatch func(func()) // webview UI 线程派发器（窗口模式才非空）
	wvEval     func(string) // 在 webview 里执行 JS（须先 Dispatch）
	wvHwnd     uintptr      // 主窗口句柄（SendInput 前置聚焦）
}

// SetWebview 窗口模式下注入 webview 桥：自动领取撞到验证码时，
// 后端直接调起前端自动滑块流程，全程无需人工操作。
func (p *Panel) SetWebview(dispatch func(func()), eval func(string), hwnd uintptr) {
	p.mu.Lock()
	p.wvDispatch, p.wvEval, p.wvHwnd = dispatch, eval, hwnd
	p.mu.Unlock()
	if prov, ok := p.providerFor(store.PZCode).(*zcode.Provider); ok {
		prov.SetCaptchaTrigger(func() {
			if dispatch == nil || eval == nil {
				return
			}
			dispatch(func() { eval("window.zcAutoClaimPending && window.zcAutoClaimPending()") })
		})
	}
}

// handleZcodeSliderRect 前端把滑块 iframe 的屏幕物理坐标发过来，
// 后端用 SendInput 拟人拖拽（trusted 事件，风控视为真人）。
func (p *Panel) handleZcodeSliderRect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		X1, Y1, X2, Y2 int
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if p.wvHwnd == 0 {
		writeErr(w, 400, "自动滑块仅窗口模式可用")
		return
	}
	autoslide.FocusWindow(p.wvHwnd)
	if err := autoslide.Drag(in.X1, in.Y1, in.X2, in.Y2); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

type traePending struct {
	machineID string
	deviceID  string
	apiHost   string
	state     string // pending | success | failed
	accountID string
	uid       string
	nickname  string
	errMsg    string
	createdAt time.Time
}

func NewPanel(cfg *config.Config, st *store.Store, pl *pool.Pool, sched *scheduler.Scheduler, sh *shares.Manager, tn *tunnel.Manager, ul *usagelog.Log) *Panel {
	return &Panel{
		cfg: cfg, store: st, pl: pl, sched: sched, shares: sh, tunnel: tn, ulog: ul,
		wbStates:  map[string]string{},
		zcodeSess: map[string]*zcode.LoginSession{},
		trae:      map[string]*traePending{},
	}
}

func randHex32() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// providerFor 取提供商接口。
func (p *Panel) providerFor(name string) provider.Provider {
	return p.pl.Provider(name)
}

func (p *Panel) poolModels(ctx context.Context) map[string][]provider.ModelInfo {
	return p.pl.AllModels(ctx)
}

// Register 挂载面板路由。
func (p *Panel) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", p.serveIndex)
	mux.HandleFunc("GET /app.js", p.serveAppJS)
	mux.HandleFunc("GET /captcha.js", p.serveCaptchaJS)
	mux.HandleFunc("GET /favicon.svg", p.serveFavicon)
	mux.HandleFunc("GET /icons/{name}", p.serveIcon)

	api := func(path string, h http.HandlerFunc) {
		mux.HandleFunc(path, p.withPanelAuth(h))
	}
	api("GET /api/panel/overview", p.handleOverview)
	api("GET /api/panel/logs", p.handleLogs)
	api("GET /api/panel/logs/stream", p.handleLogStream)
	api("GET /api/panel/models", p.handleModels)
	api("POST /api/panel/checkin-pass", p.handleCheckinPass)
	api("POST /api/panel/refresh-quotas", p.handleRefreshQuotas)
	api("POST /api/panel/refresh-tokens", p.handleRefreshTokens)
	api("GET /api/panel/config", p.handleGetConfig)
	api("POST /api/panel/config", p.handleSaveConfig)

	api("POST /api/panel/account", p.handleAccountAdd)
	api("PATCH /api/panel/account/{id}", p.handleAccountPatch)
	api("DELETE /api/panel/account/{id}", p.handleAccountDelete)
	api("POST /api/panel/account/{id}/quota", p.handleAccountQuota)
	api("GET /api/panel/account/{id}/quota-raw", p.handleAccountQuotaRaw)
	api("POST /api/panel/account/{id}/checkin", p.handleAccountCheckin)
	api("POST /api/panel/account/{id}/refresh", p.handleAccountRefresh)
	api("GET /api/panel/account/{id}/refresh-token", p.handleGetRefreshToken)
	api("POST /api/panel/account/{id}/switch-trae", p.handleAccountSwitchTrae)
	api("POST /api/panel/account/{id}/capture-trae", p.handleAccountCaptureTrae)
	api("POST /api/panel/account/{id}/migrate-sessions", p.handleMigrateSessions)
	api("GET /api/panel/trae/migrate-status", p.handleMigrateStatus)
	api("GET /api/panel/trae/dbkey", p.handleGetDBKey)
	api("POST /api/panel/trae/dbkey", p.handleSetDBKey)

	api("POST /api/panel/login/workbuddy", p.handleWBLoginStart)
	api("GET /api/panel/login/workbuddy/poll", p.handleWBLoginPoll)
	api("POST /api/panel/login/trae", p.handleTraeLoginStart)
	api("GET /api/panel/login/trae/poll", p.handleTraeLoginPoll)
	api("POST /api/panel/login/trae/callback", p.handleTraeLoginCallback)
	api("POST /api/panel/login/zcode", p.handleZcodeLoginStart)
	api("GET /api/panel/login/zcode/poll", p.handleZcodeLoginPoll)
	api("POST /api/panel/import-local/workbuddy", p.handleImportLocalWB)
	api("POST /api/panel/import-local/trae", p.handleImportLocalTrae)
	api("POST /api/panel/import-local/zcode", p.handleZcodeImportLocal)
	api("POST /api/panel/zcode/import-local", p.handleZcodeImportLocal)
	api("GET /api/panel/zcode/plans", p.handleZcodePlans)
	api("GET /api/panel/zcode/pending", p.handleZcodePending)
	api("GET /api/panel/usage-summary", p.handleUsageSummary)
	api("POST /api/panel/zcode/claim", p.handleZcodeClaim)
	api("GET /api/panel/zcode/captcha-config", p.handleZcodeCaptchaConfig)
	api("POST /api/panel/zcode/slider-rect", p.handleZcodeSliderRect)

	// 公网分享（隧道）与分享钥匙
	api("POST /api/panel/tunnel/start", p.handleTunnelStart)
	api("POST /api/panel/tunnel/stop", p.handleTunnelStop)
	api("GET /api/panel/tunnel/status", p.handleTunnelStatus)
	api("GET /api/panel/shares", p.handleSharesList)
	api("POST /api/panel/shares", p.handleShareCreate)
	api("PATCH /api/panel/shares/{id}", p.handleSharePatch)
	api("DELETE /api/panel/shares/{id}", p.handleShareDelete)
	api("GET /api/panel/usage", p.handleUsageList)
}

// ── 公网分享（隧道） ──

func (p *Panel) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	_ = p.tunnel.Start()
	st, url, errMsg := p.tunnel.Status()
	writeJSON(w, map[string]any{"state": st, "url": url, "error": errMsg})
}

func (p *Panel) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	p.tunnel.Stop()
	st, url, errMsg := p.tunnel.Status()
	writeJSON(w, map[string]any{"state": st, "url": url, "error": errMsg})
}

func (p *Panel) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	st, url, errMsg := p.tunnel.Status()
	writeJSON(w, map[string]any{"state": st, "url": url, "error": errMsg})
}

// ── 分享钥匙 ──

func (p *Panel) handleSharesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"shares": p.shares.All()})
}

// handleUsageList 用量日志（分页，新在前）。?page=1&page_size=50
func (p *Panel) handleUsageList(w http.ResponseWriter, r *http.Request) {
	if p.ulog == nil {
		writeJSON(w, map[string]any{"entries": []any{}, "total": 0, "page": 1, "pageSize": 15})
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	if page < 1 {
		page = 1
	}
	if size < 10 || size > 200 {
		size = 15
	}
	all := p.ulog.List(0)
	total := len(all)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	entries := all[start:end]
	if entries == nil {
		entries = []usagelog.Entry{}
	}
	writeJSON(w, map[string]any{"entries": entries, "total": total, "page": page, "pageSize": size})
}

func (p *Panel) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string                       `json:"name"`
		Providers map[string]*shares.ProvShare `json:"providers"`
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s, err := p.shares.Create(strings.TrimSpace(in.Name), in.Providers)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logx.Infof("panel", "创建分享钥匙 %s", s.Name)
	writeJSON(w, s)
}

func (p *Panel) handleSharePatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Enabled   *bool                        `json:"enabled"`
		Name      *string                      `json:"name"`
		Providers map[string]*shares.ProvShare `json:"providers"`
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	err := p.shares.Update(id, func(s *shares.Share) {
		if in.Enabled != nil {
			s.Enabled = *in.Enabled
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			s.Name = strings.TrimSpace(*in.Name)
		}
		for k, v := range in.Providers {
			if v == nil {
				continue
			}
			ps := s.Prov[k]
			if ps == nil {
				ps = &shares.ProvShare{Enabled: v.Enabled, Limit: v.Limit}
			} else {
				ps.Enabled = v.Enabled
				ps.Limit = v.Limit
			}
			// 模型白名单：nil 或空数组都视为不限
			ps.Models = v.Models
			s.Prov[k] = ps
		}
	})
	if err != nil {
		writeErr(w, 404, "分享钥匙不存在")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (p *Panel) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := p.shares.Delete(id); err != nil {
		writeErr(w, 404, "分享钥匙不存在")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// withPanelAuth 回环访问免鉴权，非回环要求 Bearer key。
func (p *Panel) withPanelAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		addr := r.RemoteAddr
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		loopback := host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "::ffff:127.")
		if loopback {
			next(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == p.cfg.APIKey {
			next(w, r)
			return
		}
		writeErr(w, 401, "需要面板密钥")
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// ── 静态资源 ──

func (p *Panel) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexHTML)
}

func (p *Panel) serveAppJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(appJS)
}

func (p *Panel) serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(faviconSVG))
}

// ── 总览 / 日志 / 模型 / 配置 ──

type provSummary struct {
	Accounts int     `json:"accounts"`
	Active   int     `json:"active"`
	Remain   float64 `json:"remain"`
	Total    float64 `json:"total"`
	Unit     string  `json:"unit,omitempty"`
}

func (p *Panel) handleOverview(w http.ResponseWriter, r *http.Request) {
	accounts := p.store.All()
	sums := map[string]*provSummary{}
	for _, name := range config.AllProviders() {
		sums[name] = &provSummary{}
	}
	for _, a := range accounts {
		s := sums[a.Provider]
		if s == nil {
			continue
		}
		s.Accounts++
		if a.Enabled && !a.Dead {
			s.Active++
		}
		if a.LastQuota != nil {
			s.Remain += a.LastQuota.Remain
			s.Total += a.LastQuota.Total
			if s.Unit == "" {
				s.Unit = a.LastQuota.Unit
			}
		}
	}
	writeJSON(w, map[string]any{
		"app":      config.AppName,
		"version":  config.AppVersion,
		"apiKey":   p.cfg.APIKey,
		"host":     p.cfg.Host,
		"port":     p.cfg.Port,
		"accounts": accounts,
		"summary":  sums,
		"localIP":  localIP(),
		// 页面跑在内嵌 WebView 里（而非外部浏览器）时才允许自动滑块
		"webviewWindow": p.wvDispatch != nil,
	})
}

func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func (p *Panel) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 300
	if v := r.URL.Query().Get("n"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	writeJSON(w, map[string]any{"entries": logx.Last(n)})
}

func (p *Panel) handleLogStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "stream unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	ch := logx.Subscribe()
	defer logx.Unsubscribe(ch)
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			raw, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			fl.Flush()
		}
	}
}

func (p *Panel) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"models": p.poolModels(r.Context())})
}

func (p *Panel) handleCheckinPass(w http.ResponseWriter, r *http.Request) {
	// 同步等待一轮签到完成（前端按钮显示进行中）；超时兜底防止请求悬挂。
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	p.sched.RunCheckinPass(ctx, true)
	writeJSON(w, map[string]any{"done": true})
}

func (p *Panel) handleRefreshQuotas(w http.ResponseWriter, r *http.Request) {
	// 同步等待全部额度刷新完成再返回，前端拿到响应时快照已落库。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p.sched.RunQuotaRefresh(ctx)
	writeJSON(w, map[string]any{"done": true})
}

// refreshTokResult 单账号批量刷新结果。
type refreshTokResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Err  string `json:"error,omitempty"`
}

// handleRefreshTokens 批量刷新全部账号 token（refreshToken 优先，缺失/失效回退本机同步）。
// 已停用账号跳过；凭据失效（Dead）的账号也尝试——本机同步成功即复活。
func (p *Panel) handleRefreshTokens(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	accts := p.store.All()
	sem := make(chan struct{}, 3) // 并发 3，与额度刷新一致
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]refreshTokResult, 0, len(accts))
	for _, acct := range accts {
		if !acct.Enabled {
			continue // 用户手动停用的不碰
		}
		prov := p.providerFor(acct.Provider)
		if prov == nil {
			continue
		}
		// 无 refreshToken 且提供商不支持本机同步 → 无刷新途径，跳过
		if acct.RefreshToken == "" {
			if _, ok := prov.(provider.LocalSyncer); !ok {
				continue
			}
		}
		wg.Add(1)
		go func(a *store.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := p.pl.RefreshAccount(ctx, a)
			mu.Lock()
			defer mu.Unlock()
			res := refreshTokResult{ID: a.ID, Name: a.DisplayedName(), OK: err == nil}
			if err != nil {
				res.Err = err.Error()
			} else {
				if a.Dead { // 凭据已恢复，复活账号
					a.Dead = false
					a.DeadReason = ""
				}
				_ = p.store.Upsert(a)
			}
			results = append(results, res)
		}(acct)
	}
	wg.Wait()
	okN, failN := 0, 0
	for _, res := range results {
		if res.OK {
			okN++
		} else {
			failN++
		}
	}
	writeJSON(w, map[string]any{"results": results, "ok": okN, "failed": failN})
}

func (p *Panel) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, p.cfg)
}

func (p *Panel) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// 不允许通过面板修改数据目录
	delete(in, "DataDir")
	raw, _ := json.Marshal(in)
	if err := json.Unmarshal(raw, p.cfg); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	p.cfg.Normalize()
	if err := p.cfg.Save(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logx.Infof("panel", "配置已保存（部分项重启后生效）")
	writeJSON(w, map[string]any{"ok": true})
}

// ── 账号 CRUD ──

func (p *Panel) handleAccountAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider     string            `json:"provider"`
		Realm        string            `json:"realm"`
		AccessToken  string            `json:"accessToken"`
		RefreshToken string            `json:"refreshToken"`
		UID          string            `json:"uid"`
		Nickname     string            `json:"nickname"`
		ExpiresAt    int64             `json:"expiresAt"`
		Extra        map[string]string `json:"extra"`
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.Provider != store.PWorkBuddy && in.Provider != store.PTrae && in.Provider != store.PZCode {
		writeErr(w, 400, "provider 必须是 workbuddy / trae / zcode")
		return
	}
	if strings.TrimSpace(in.AccessToken) == "" && strings.TrimSpace(in.RefreshToken) == "" {
		writeErr(w, 400, "accessToken / refreshToken 至少填一个")
		return
	}
	acct := &store.Account{
		ID:           store.NewID(),
		Provider:     in.Provider,
		Realm:        in.Realm,
		UID:          in.UID,
		Nickname:     in.Nickname,
		AccessToken:  strings.TrimSpace(in.AccessToken),
		RefreshToken: strings.TrimSpace(in.RefreshToken),
		ExpiresAt:    in.ExpiresAt,
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
		Extra:        in.Extra,
	}
	if err := p.store.Upsert(acct); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logx.Infof("panel", "手动导入 %s 账号 %s", in.Provider, acct.DisplayedName())
	writeJSON(w, map[string]any{"ok": true, "id": acct.ID})
}

func (p *Panel) handleAccountPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	err := p.store.Mutate(id, func(a *store.Account) {
		if in.Enabled != nil {
			a.Enabled = *in.Enabled
			if a.Enabled {
				a.Dead = false
				a.DeadReason = ""
				a.CooldownUntil = 0
			}
		}
	})
	if err != nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (p *Panel) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := p.store.Delete(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (p *Panel) handleAccountQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := p.store.Get(id)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	prov := p.providerFor(acct.Provider)
	if prov == nil {
		writeErr(w, 400, "提供商不可用")
		return
	}
	snap, err := prov.Quota(r.Context(), acct)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	_ = p.store.Mutate(id, func(a *store.Account) { a.LastQuota = snap })
	writeJSON(w, snap)
}

// handleAccountQuotaRaw 返回 ZCode 账号额度相关上游接口的原始响应（诊断用）。
func (p *Panel) handleAccountQuotaRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := p.store.Get(id)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	prov, ok := p.providerFor(store.PZCode).(*zcode.Provider)
	if !ok || acct.Provider != store.PZCode {
		writeErr(w, 400, "仅支持 ZCode 账号")
		return
	}
	writeJSON(w, prov.RawQuotaSources(r.Context(), acct))
}

func (p *Panel) handleAccountCheckin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := p.store.Get(id)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	prov := p.providerFor(acct.Provider)
	if prov == nil {
		writeErr(w, 400, "提供商不可用")
		return
	}
	already, msg, err := prov.Checkin(r.Context(), acct)
	// Checkin 内部可能补了 deviceId（9004 根因），无论成败都落库
	if did := acct.ExtraGet(store.ExtraTraeDeviceID); did != "" {
		_ = p.store.Mutate(id, func(a *store.Account) { a.ExtraSet(store.ExtraTraeDeviceID, did) })
	}
	result := map[string]any{"already": already, "msg": msg}
	if err != nil {
		result["error"] = err.Error()
	} else {
		today := time.Now().Format("2006-01-02")
		_ = p.store.Mutate(id, func(a *store.Account) {
			a.LastCheckinAt = time.Now().Unix()
			a.LastCheckinMsg = msg
			if acct.Provider != store.PZCode {
				a.LastCheckinDay = today
			}
		})
		if snap, qerr := prov.Quota(r.Context(), acct); qerr == nil {
			_ = p.store.Mutate(id, func(a *store.Account) { a.LastQuota = snap })
		}
	}
	writeJSON(w, result)
}

// handleGetRefreshToken 返回账号的 refreshToken（供用户复制导出）。
// 面板仅监听本机/局域网，凭据不暴露公网（安全红线）。
func (p *Panel) handleGetRefreshToken(w http.ResponseWriter, r *http.Request) {
	acct := p.store.Get(r.PathValue("id"))
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	writeJSON(w, map[string]any{"refreshToken": acct.RefreshToken})
}

// saveTraeSnapshot 把本机 Trae 当前登录信封存进账号（会话入库，切换来回不丢）。
// UID 双方都有且不一致时报错；账号无 UID 时直接存（信封为准）。
func (p *Panel) saveTraeSnapshot(target *store.Account, snap *traeswitch.Snapshot) error {
	if snap == nil || snap.Auth == "" {
		return fmt.Errorf("本机 Trae 未登录（storage.json 无登录信封）")
	}
	if target.UID != "" && snap.UID != "" && snap.UID != target.UID {
		return fmt.Errorf("本机 Trae 当前登录的不是该账号（uid %s），请先用「抓会话」或重新导入", snap.UID)
	}
	_ = p.store.Mutate(target.ID, func(a *store.Account) {
		a.ExtraSet(store.ExtraTraeAuthEnvelope, snap.Auth)
		if snap.Ent != "" {
			a.ExtraSet(store.ExtraTraeEntEnvelope, snap.Ent)
		}
		if snap.Server != "" {
			a.ExtraSet(store.ExtraTraeServerEnvelope, snap.Server)
		}
		if snap.Token != "" {
			a.AccessToken = snap.Token
			if a.UID == "" {
				a.UID = snap.UID
			}
			a.Dead = false
			a.DeadReason = ""
		}
	})
	return nil
}

// handleAccountCaptureTrae 把本机 Trae 当前登录会话抓取进指定账号（信封 + 最新 token）。
func (p *Panel) handleAccountCaptureTrae(w http.ResponseWriter, r *http.Request) {
	acct := p.store.Get(r.PathValue("id"))
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	if acct.Provider != store.PTrae {
		writeErr(w, 400, "仅支持 Trae 账号")
		return
	}
	snap, err := traeswitch.SnapshotIDE()
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if err := p.saveTraeSnapshot(acct, snap); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	logx.Infof(store.PTrae, "账号 %s 已抓取本机 Trae 登录会话", acct.DisplayedName())
	writeJSON(w, map[string]any{"ok": true})
}

// handleAccountSwitchTrae 一键把本机 Trae 客户端切换为指定账号登录。
// 流程：先快照当前 IDE 登录（会话入库；池里没有就自动补一条），再写目标账号信封并重启 IDE。
func (p *Panel) handleAccountSwitchTrae(w http.ResponseWriter, r *http.Request) {
	acct := p.store.Get(r.PathValue("id"))
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	if acct.Provider != store.PTrae {
		writeErr(w, 400, "仅支持 Trae 账号")
		return
	}
	snap, err := traeswitch.SnapshotIDE()
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	// 切换前保存当前登录会话（会话不丢的关键一步）
	if snap.Auth != "" && snap.UID != "" && snap.UID != acct.UID {
		matched := false
		for _, a := range p.store.ByProvider(store.PTrae) {
			if a.UID == snap.UID {
				if err := p.saveTraeSnapshot(a, snap); err == nil {
					matched = true
				}
				break
			}
		}
		if !matched {
			// 池里没有当前 IDE 登录的账号 → 自动补一条（与「从本机导入」同语义，不碰 refreshToken）
			na := &store.Account{
				ID: store.NewID(), Provider: store.PTrae, Realm: "cn",
				UID: snap.UID, AccessToken: snap.Token, Enabled: true,
				CreatedAt: time.Now().Unix(),
			}
			na.ExtraSet(store.ExtraTraeAuthEnvelope, snap.Auth)
			if snap.Ent != "" {
				na.ExtraSet(store.ExtraTraeEntEnvelope, snap.Ent)
			}
			if snap.Server != "" {
				na.ExtraSet(store.ExtraTraeServerEnvelope, snap.Server)
			}
			if snap.Host != "" {
				na.ExtraSet(store.ExtraTraeAPIHost, snap.Host)
			}
			_ = p.store.Upsert(na)
			logx.Infof(store.PTrae, "切换前自动入库当前 Trae 登录: uid %s", snap.UID)
		}
	}
	if snap.UID != "" && snap.UID == acct.UID {
		// 目标账号就是当前登录：仅刷新其信封快照，不重启
		if err := p.saveTraeSnapshot(acct, snap); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "already": true})
		return
	}
	machineID, err := traeswitch.Apply(acct)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	_ = p.store.Upsert(acct) // machineId 已回写
	pre := machineID
	if len(pre) > 8 {
		pre = pre[:8]
	}
	logx.Infof(store.PTrae, "本机 Trae 已切换为账号 %s（machineId %s…）", acct.DisplayedName(), pre)
	writeJSON(w, map[string]any{"ok": true, "machineId": machineID})
}

// handleMigrateSessions 启动 Trae AI 会话迁移（异步，进度走 migrate-status 轮询）。
// 把本机 Trae 当前登录账号的全部 AI 会话迁给指定账号（需 SQLCipher 密钥，见面板设置）。
func (p *Panel) handleMigrateSessions(w http.ResponseWriter, r *http.Request) {
	acct := p.store.Get(r.PathValue("id"))
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	if acct.Provider != store.PTrae {
		writeErr(w, 400, "仅支持 Trae 账号")
		return
	}
	if acct.UID == "" {
		writeErr(w, 400, "该账号缺少 uid，无法作为迁移目标")
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in)
	if err := traemigrate.StartMigrate(acct, p.cfg.DataDir, in.Key); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// handleMigrateStatus 迁移任务进度（前端 1s 轮询渲染日志）。
func (p *Panel) handleMigrateStatus(w http.ResponseWriter, r *http.Request) {
	running, logs, result := traemigrate.JobStatus()
	writeJSON(w, map[string]any{"running": running, "logs": logs, "result": result})
}

// handleGetDBKey 返回数据库密钥配置状态（只回尾4位，不回全量）。
func (p *Panel) handleGetDBKey(w http.ResponseWriter, r *http.Request) {
	k := traemigrate.LoadDBKey(p.cfg.DataDir)
	hint := ""
	if k != "" {
		hint = "…" + k[len(k)-4:]
	}
	writeJSON(w, map[string]any{"set": k != "", "hint": hint})
}

// handleSetDBKey 保存 Trae AI 库 SQLCipher 密钥（64 位 hex）。
func (p *Panel) handleSetDBKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		writeErr(w, 400, "参数错误")
		return
	}
	if err := traemigrate.SaveDBKey(p.cfg.DataDir, in.Key); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// UsageAccount 用量页单账号额度行。
type UsageAccount struct {
	ID        string               `json:"id"`
	Provider  string               `json:"provider"`
	Nickname  string               `json:"nickname"`
	Plan      string               `json:"plan,omitempty"`
	Total     float64              `json:"total"`
	Used      float64              `json:"used"`
	Remain    float64              `json:"remain"`
	Unit      string               `json:"unit,omitempty"`
	UpdatedAt int64                `json:"updatedAt,omitempty"`
	Parts     []store.QuotaPart    `json:"parts,omitempty"`
	Quota     *store.QuotaSnapshot `json:"-"`
}

// handleUsageSummary 用量页数据：各账号额度 + 最近调用按调用方/提供商/模型聚合。
func (p *Panel) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	// 账号额度（取最近快照）
	var accounts []UsageAccount
	for _, st := range p.pl.Status() {
		if st.Quota == nil {
			continue
		}
		q := st.Quota
		accounts = append(accounts, UsageAccount{
			ID: st.ID, Provider: st.Provider, Nickname: st.Nickname,
			Plan: q.Plan, Total: q.Total, Used: q.Used, Remain: q.Remain,
			Unit: q.Unit, UpdatedAt: q.UpdatedAt, Parts: q.Parts,
		})
	}
	if accounts == nil {
		accounts = []UsageAccount{}
	}
	// 最近调用量（usagelog 环形缓冲，最多 500 条）
	entries := p.ulog.List(0)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	weekStart := now.Unix() - 7*86400
	// 今日卡片用持久化台账（不受环形缓冲 500 条裁剪影响，跨重启保留）；
	// 近 7 天仍按缓冲内条目聚合（只作趋势参考）。
	var todayTokens, todayReqs int64
	var cacheRead, cacheWrite, cacheIn float64
	if td := p.ulog.Today(); td.Day == now.Format("2006-01-02") {
		todayTokens, todayReqs = td.Tokens, td.Reqs
		cacheRead, cacheWrite = float64(td.CacheRead), float64(td.CacheWrite)
		cacheIn = float64(td.InTokens + td.CacheRead + td.CacheWrite)
	}
	cacheHit := 0.0
	if cacheIn > 0 {
		cacheHit = cacheRead / cacheIn
	}
	type agg struct {
		Tokens   int64   `json:"tokens"`
		Requests int64   `json:"requests"`
		Ok       int64   `json:"ok"`
	}
	var week agg
	byCaller := map[string]*agg{}
	byProvider := map[string]*agg{}
	byModel := map[string]*agg{}
	get := func(m map[string]*agg, k string) *agg {
		if m[k] == nil {
			m[k] = &agg{}
		}
		return m[k]
	}
	for _, e := range entries {
		if !e.Ok {
			continue
		}
		a := agg{Tokens: e.TotalTokens, Requests: 1, Ok: 1}
		if e.Time >= todayStart {
			week.Tokens += a.Tokens
			week.Requests++
		}
		if e.Time >= weekStart {
			week.Tokens += a.Tokens
			week.Requests++
		}
		caller := e.Caller
		if caller == "" {
			caller = "owner"
		}
		*get(byCaller, caller) = agg{get(byCaller, caller).Tokens + a.Tokens, get(byCaller, caller).Requests + 1, 0}
		*get(byProvider, e.Provider) = agg{get(byProvider, e.Provider).Tokens + a.Tokens, get(byProvider, e.Provider).Requests + 1, 0}
		*get(byModel, e.Model) = agg{get(byModel, e.Model).Tokens + a.Tokens, get(byModel, e.Model).Requests + 1, 0}
	}
	var totalReqs, totalOk int64
	for _, e := range entries {
		totalReqs++
		if e.Ok {
			totalOk++
		}
	}
	// map → 有序切片（按 tokens 降序）
	type row struct {
		Key      string `json:"key"`
		Tokens   int64  `json:"tokens"`
		Requests int64  `json:"requests"`
	}
	toRows := func(m map[string]*agg) []row {
		out := []row{}
		for k, v := range m {
			out = append(out, row{k, v.Tokens, v.Requests})
		}
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				if out[j].Tokens > out[i].Tokens {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		return out
	}
	writeJSON(w, map[string]any{
		"accounts": accounts,
		"tokens": map[string]any{
			"today": todayTokens, "todayReqs": todayReqs, "week": week.Tokens,
			"cacheHit": cacheHit, "cacheRead": int64(cacheRead), "cacheWrite": int64(cacheWrite),
			"requests": totalReqs, "ok": totalOk, "tracked": len(entries),
		},
		"byCaller":   toRows(byCaller),
		"byProvider": toRows(byProvider),
		"byModel":    toRows(byModel),
	})
}

func (p *Panel) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct := p.store.Get(id)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	if p.providerFor(acct.Provider) == nil {
		writeErr(w, 400, "提供商不可用")
		return
	}
	// 统一刷新策略：refreshToken 优先，缺失/失效时回退本机客户端同步
	if err := p.pl.RefreshAccount(r.Context(), acct); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	_ = p.store.Upsert(acct)
	writeJSON(w, map[string]any{"ok": true, "expiresAt": acct.ExpiresAt})
}
