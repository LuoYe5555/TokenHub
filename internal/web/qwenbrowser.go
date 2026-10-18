// 千问网页版一键浏览器导入：
// start → 拉起系统 Edge/Chrome（专用配置目录 + CDP 调试端口）打开 chat.qwen.ai，
// 用户在窗口里正常登录；前端每 2 秒 poll 一次，登录成功（拿到 token cookie）后
// 自动建号/更新。免 F12 手动抓包。参考 qwengate 的浏览器配置文件方案。
package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/provider/qwen"
	"tokenhub/internal/qwencdp"
	"tokenhub/internal/store"
)

const qwenProfileDir = "data/qwen-profile"

var qwenBr struct {
	mu   sync.Mutex
	port int
	proc *os.Process
	acct string // 过验证流程中待处理的账号 ID
}

// handleQwenBrowserStart 拉起（或重启）导入浏览器窗口。
func (p *Panel) handleQwenBrowserStart(w http.ResponseWriter, r *http.Request) {
	qwenBr.mu.Lock()
	defer qwenBr.mu.Unlock()
	// 关掉上一轮导入窗口，避免同配置目录的旧进程吞掉新调试端口
	qwenBrStopLocked()
	exe := qwencdp.FindBrowser()
	if exe == "" {
		writeJSON(w, map[string]any{"ok": false, "status": "no-browser"})
		return
	}
	port, err := qwencdp.FreePort()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "取空闲端口失败: "+err.Error())
		return
	}
	profile, _ := filepath.Abs(qwenProfileDir)
	cmd, err := qwencdp.Launch(exe, profile, port, "https://chat.qwen.ai/", false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "启动浏览器失败: "+err.Error())
		return
	}
	qwenBr.port = port
	if cmd.Process != nil {
		qwenBr.proc = cmd.Process
	}
	if !qwencdp.WaitDebug(port, 25*time.Second) {
		writeJSON(w, map[string]any{"ok": false, "status": "debug-timeout"})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleQwenBrowserPoll 轮询登录结果：拿到 token cookie 即建号/更新。
func (p *Panel) handleQwenBrowserPoll(w http.ResponseWriter, r *http.Request) {
	qwenBr.mu.Lock()
	port := qwenBr.port
	qwenBr.mu.Unlock()
	if port == 0 {
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	cookies, err := qwencdp.GetCookies(port)
	if err != nil {
		// 窗口被关/还没就绪都归为等待，前端继续轮询
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	token, refresh := qwencdp.PickAuth(cookies)
	if token == "" && refresh != "" {
		// 新版千问登录态只有 refresh_token cookie：换短期 access token
		if res, err := qwen.ExchangeRefresh(r.Context(), "https://chat.qwen.ai", refresh); err == nil && res.Token != "" {
			token = res.Token
			if res.RefreshToken != "" {
				refresh = res.RefreshToken
			}
		}
	}
	if token == "" {
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	// 全套 qwen.ai 风控 cookie（cna/tfstk/isg…）：请求时原样带上以贴近浏览器形状
	wafCookie := qwencdp.CookieHeader(cookies, "chat.qwen.ai")
	// 阿里风控签名（bx-ua/bx-umidtoken 由站点 JS 生成）：刷新页面后抓一条真实请求头
	signatures := qwenSniffSignatures(port)
	nick := qwenNickFromJWT(token)
	acctID := ""
	for _, a := range p.store.All() {
		if a.Provider != store.PQwen {
			continue
		}
		// refresh token 是长期身份；token 每次换新都会变，不能按 token 匹配
		if refresh != "" && a.RefreshToken == refresh {
			acctID = a.ID
			break
		}
		if acctID == "" && a.AccessToken == token {
			acctID = a.ID
		}
	}
	if acctID != "" {
		_ = p.store.Mutate(acctID, func(a *store.Account) {
			a.AccessToken = token
			if refresh != "" {
				a.RefreshToken = refresh
			}
			if a.Nickname == "" && nick != "" {
				a.Nickname = nick
			}
			qwenSetWafCookie(a, wafCookie)
			qwenSetSignatures(a, signatures)
			// 重新导入视为人工修复动作：清掉风控冷却，立即可试
			a.CooldownUntil = 0
			a.CooldownReason = ""
		})
	} else {
		if refresh == "" {
			refresh = token
		}
		acct := &store.Account{
			ID:           store.NewID(),
			Provider:     store.PQwen,
			AccessToken:  token,
			RefreshToken: refresh,
			Nickname:     nick,
			Enabled:      true,
			CreatedAt:    time.Now().Unix(),
		}
		qwenSetWafCookie(acct, wafCookie)
		qwenSetSignatures(acct, signatures)
		if err := p.store.Upsert(acct); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		acctID = acct.ID
	}
	// sig 告知前端风控签名（bx-ua）是否抓到：抓不到说明页面没发出带签名的
	// API 请求（可能弹了滑块/加载慢），用户需要重试导入
	writeJSON(w, map[string]any{"ok": true, "id": acctID, "nickname": nick, "sig": len(signatures) > 0})
}

// qwenSetWafCookie 更新账号的风控 cookie；空值不覆盖旧值。
func qwenSetWafCookie(a *store.Account, v string) {
	if v == "" {
		return
	}
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	a.Extra["wafCookie"] = v
}

// 签名嗅探节流：poll 每 2 秒跑一次，刷新页面抓头 30 秒最多做一次。
var (
	qwenSniffMu sync.Mutex
	qwenSniffAt time.Time
)

// qwenSniffSignatures 刷新导入窗口页面并抓一条真实 /api/ 请求头；
// 离上次嗅探不足 30 秒时返回 nil（沿用已存值）。
func qwenSniffSignatures(port int) map[string]string {
	qwenSniffMu.Lock()
	due := time.Since(qwenSniffAt) > 30*time.Second
	if due {
		qwenSniffAt = time.Now()
	}
	qwenSniffMu.Unlock()
	if !due {
		return nil
	}
	return qwencdp.SniffAPIHeaders(port, "chat.qwen.ai", 8*time.Second, true)
}

// qwenSetSignatures 把抓到的风控签名写进账号（非空才覆盖）。
func qwenSetSignatures(a *store.Account, h map[string]string) {
	if len(h) == 0 {
		return
	}
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	pairs := [][2]string{
		{"bx-ua", "bxUa"},
		{"bx-umidtoken", "bxUmid"},
		{"bx-v", "bxV"},
		{"version", "siteVer"},
	}
	for _, p := range pairs {
		if v := h[p[0]]; v != "" {
			a.Extra[p[1]] = v
		}
	}
}

// qwenNickFromJWT 令牌是 JWT 时解出 email/name 当备注名；失败留空（Quota 刷新会补）。
func qwenNickFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	if claims.Name != "" {
		return claims.Name
	}
	return claims.Email
}

func qwenBrStopLocked() {
	if qwenBr.proc != nil {
		_ = qwenBr.proc.Kill()
		qwenBr.proc = nil
	}
	qwenBr.port = 0
	qwenBr.acct = ""
}

// handleQwenVerifyStart 打开 WAF punish 验证页（滑块）。punish 页带 x5secdata
// 令牌，在同一浏览器配置目录里过一次验证会种下 x5sec cookie，这就是 WAF
// 认可的"人类凭证"——比形状伪装更彻底。
func (p *Panel) handleQwenVerifyStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	target, acctID := "", req.ID
	if acctID != "" {
		for _, a := range p.store.All() {
			if a.ID == acctID && a.Provider == store.PQwen {
				target = a.Extra["punishURL"]
				break
			}
		}
	} else {
		// 未指定账号：取第一个待验证的启用账号
		for _, a := range p.store.All() {
			if a.Provider == store.PQwen && a.Enabled && a.Extra["punishURL"] != "" {
				target, acctID = a.Extra["punishURL"], a.ID
				break
			}
		}
	}
	if target == "" {
		writeJSON(w, map[string]any{"ok": false, "status": "no-punish"})
		return
	}
	qwenBr.mu.Lock()
	qwenBrStopLocked()
	exe := qwencdp.FindBrowser()
	if exe == "" {
		qwenBr.mu.Unlock()
		writeJSON(w, map[string]any{"ok": false, "status": "no-browser"})
		return
	}
	port, err := qwencdp.FreePort()
	if err != nil {
		qwenBr.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "取空闲端口失败: "+err.Error())
		return
	}
	profile, _ := filepath.Abs(qwenProfileDir)
	cmd, err := qwencdp.Launch(exe, profile, port, target, false)
	if err != nil {
		qwenBr.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "启动浏览器失败: "+err.Error())
		return
	}
	qwenBr.port = port
	qwenBr.acct = acctID
	if cmd.Process != nil {
		qwenBr.proc = cmd.Process
	}
	qwenBr.mu.Unlock()
	if !qwencdp.WaitDebug(port, 25*time.Second) {
		writeJSON(w, map[string]any{"ok": false, "status": "debug-timeout"})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleQwenVerifyPoll 轮询验证窗口：一旦出现 x5sec cookie 即视为通过——
// 重建全套风控 cookie（含 x5sec）、刷新签名、清冷却与 punish 标记。
func (p *Panel) handleQwenVerifyPoll(w http.ResponseWriter, r *http.Request) {
	qwenBr.mu.Lock()
	port, acctID := qwenBr.port, qwenBr.acct
	qwenBr.mu.Unlock()
	if port == 0 || acctID == "" {
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	cookies, err := qwencdp.GetCookies(port)
	if err != nil {
		// 窗口被关/未就绪都归为等待
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	x5sec := ""
	for _, c := range cookies {
		if strings.EqualFold(c.Name, "x5sec") && c.Value != "" {
			x5sec = c.Value
			break
		}
	}
	if x5sec == "" {
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	// 通过：重建全套 cookie；x5sec 若落在别的域（如 .taobao.com）补一份
	wafCookie := qwencdp.CookieHeader(cookies, "chat.qwen.ai")
	if wafCookie != "" && !strings.Contains(wafCookie, "x5sec=") {
		wafCookie += "; x5sec=" + x5sec
	}
	// 验证后页面会重载，顺手抓一条新签名（失败不影响，沿用旧值）
	sig := qwencdp.SniffAPIHeaders(port, "chat.qwen.ai", 6*time.Second, true)
	err = p.store.Mutate(acctID, func(a *store.Account) {
		qwenSetWafCookie(a, wafCookie)
		qwenSetSignatures(a, sig)
		if a.Extra != nil {
			delete(a.Extra, "punishURL")
		}
		a.CooldownUntil = 0
		a.CooldownReason = ""
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
