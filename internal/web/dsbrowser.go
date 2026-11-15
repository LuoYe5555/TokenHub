// DeepSeek 网页版一键浏览器导入：
// start → 拉起系统 Edge/Chrome（专用配置目录 + CDP 调试端口）打开 chat.deepseek.com，
// 用户在窗口里正常登录（手机号/邮箱验证码）；前端每 2 秒 poll 一次，
// 登录成功（localStorage 出现 userToken）后自动建号/更新。免 F12 手动抓包。
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/logx"
	"tokenhub/internal/qwencdp"
	"tokenhub/internal/store"
)

const (
	dsProfileDir  = "data/deepseek-profile"
	dsHostPart    = "chat.deepseek.com"
	dsSiteURL     = "https://chat.deepseek.com/"
	dsTokenMaxAge = 30 * time.Minute
)

var dsBr struct {
	mu   sync.Mutex
	port int
	proc *os.Process
}

// lastDSLog poll 节流日志时间戳
var lastDSLog atomic.Value

func init() { lastDSLog.Store(time.Time{}) }

// dsTokenRe DeepSeek token 形状（长 hex / JWT-ish）
var dsTokenRe = regexp.MustCompile(`^[A-Za-z0-9._-]{40,}$`)

func isPlausibleDSToken(s string) bool { return dsTokenRe.MatchString(strings.TrimSpace(s)) }

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// handleDSBrowserStart 拉起（或重启）导入浏览器窗口。
func (p *Panel) handleDSBrowserStart(w http.ResponseWriter, r *http.Request) {
	dsBr.mu.Lock()
	defer dsBr.mu.Unlock()
	// 关掉上一轮导入窗口，避免同配置目录的旧进程吞掉新调试端口
	dsBrStopLocked()
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
	profile, _ := filepath.Abs(dsProfileDir)
	cmd, err := qwencdp.Launch(exe, profile, port, dsSiteURL, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "启动浏览器失败: "+err.Error())
		return
	}
	dsBr.port = port
	if cmd.Process != nil {
		dsBr.proc = cmd.Process
	}
	if !qwencdp.WaitDebug(port, 25*time.Second) {
		writeJSON(w, map[string]any{"ok": false, "status": "debug-timeout"})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleDSBrowserPoll 轮询登录结果：localStorage 出现 userToken 即建号/更新。
func (p *Panel) handleDSBrowserPoll(w http.ResponseWriter, r *http.Request) {
	dsBr.mu.Lock()
	port := dsBr.port
	dsBr.mu.Unlock()
	if port == 0 {
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	// userToken = {"value":{"token":"...","device_id":...}}
	raw, err := qwencdp.GetLocalStorageValue(port, dsHostPart, dsSiteURL, "userToken", 3*time.Second)
	if err != nil || raw == "" {
		// 节流日志：每 10 秒最多记一条，避免轮询刷屏
		if time.Since(lastDSLog.Load().(time.Time)) > 10*time.Second {
			lastDSLog.Store(time.Now())
			logx.Infof("panel", "DeepSeek 导入 poll：尚未取到 userToken（port=%d err=%v raw空=%v）", port, err, raw == "")
		}
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}
	logx.Infof("panel", "DeepSeek 导入 poll：取到 userToken（len=%d 前缀=%q）", len(raw), truncStr(raw, 60))
	// 实测形状：{"value":"<token 字符串>","__version":"0"}；兼容旧的 {"value":{"token":"..."}} 包裹
	var envStr struct {
		Value string `json:"value"`
	}
	var envObj struct {
		Value struct {
			Token    string `json:"token"`
			DeviceID string `json:"device_id"`
		} `json:"value"`
	}
	token := ""
	if json.Unmarshal([]byte(raw), &envStr) == nil && isPlausibleDSToken(envStr.Value) {
		token = strings.TrimSpace(envStr.Value)
	} else if json.Unmarshal([]byte(raw), &envObj) == nil && envObj.Value.Token != "" {
		token = envObj.Value.Token
	} else if isPlausibleDSToken(raw) {
		token = strings.TrimSpace(raw) // 兜底：整个值就是裸 token
	} else {
		logx.Infof("panel", "DeepSeek 导入 poll：userToken 形状无法解析: %q", truncStr(raw, 120))
		writeJSON(w, map[string]any{"ok": false, "status": "waiting"})
		return
	}

	// 风控 cookie 全套带上（贴近浏览器形状，降低风控评分）；拿不到不影响
	cookie := ""
	if cookies, err := qwencdp.GetCookies(port); err == nil {
		cookie = qwencdp.CookieHeader(cookies, dsHostPart)
	}

	// 拉账号信息当昵称（失败不影响导入；实测无 nickname 字段，退回手机号）
	nick := ""
	if user, err := dsFetchUser(r.Context(), token, cookie); err == nil {
		if bd, ok := user["data"].(map[string]any); ok {
			if bzd, ok := bd["biz_data"].(map[string]any); ok {
				if v, ok := bzd["nickname"].(string); ok && v != "" {
					nick = v
				} else if v, ok := bzd["mobile_number"].(string); ok && v != "" {
					nick = "DS-" + v
				}
			}
		}
	}

	acctID := ""
	for _, a := range p.store.All() {
		if a.Provider != store.PDeepSeek {
			continue
		}
		if a.AccessToken == token {
			acctID = a.ID
			break
		}
		if acctID == "" {
			acctID = a.ID // 同渠道已有账号：视为重新导入修复（token 更新）
		}
	}
	if acctID != "" {
		_ = p.store.Mutate(acctID, func(a *store.Account) {
			a.AccessToken = token
			if nick != "" {
				a.Nickname = nick
			}
			dsSetCookie(a, cookie)
			// 重新导入视为人工修复动作：清掉冷却，立即可试
			a.CooldownUntil = 0
			a.CooldownReason = ""
			a.Dead = false
			a.DeadReason = ""
		})
	} else {
		acct := &store.Account{
			ID:          store.NewID(),
			Provider:    store.PDeepSeek,
			AccessToken: token,
			Nickname:    nick,
			Enabled:     true,
			CreatedAt:   time.Now().Unix(),
		}
		dsSetCookie(acct, cookie)
		if err := p.store.Upsert(acct); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		acctID = acct.ID
	}
	writeJSON(w, map[string]any{"ok": true, "id": acctID, "nickname": nick})
}

func dsBrStopLocked() {
	if dsBr.proc != nil {
		_ = dsBr.proc.Kill()
		dsBr.proc = nil
	}
	dsBr.port = 0
}

// dsSetCookie 保存导入窗口抓的全套站点 cookie；空值不覆盖旧值。
func dsSetCookie(a *store.Account, v string) {
	if v == "" {
		return
	}
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	a.Extra["dsCookie"] = v
}

// dsFetchUser 用临时 token 拉账号信息（GET /api/v0/users/current）。
func dsFetchUser(ctx context.Context, token, cookie string) (map[string]any, error) {
	h := map[string]string{
		"authorization":      "Bearer " + token,
		"content-type":       "application/json",
		"origin":             "https://chat.deepseek.com",
		"referer":            "https://chat.deepseek.com/",
		"user-agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
		"x-client-platform":  "web",
		"x-client-version":   "2.0.0",
		"x-client-locale":    "zh-CN",
		"x-app-version":      "2.0.0",
		"x-client-timezone-offset": "28800",
	}
	if cookie != "" {
		h["cookie"] = cookie
	}
	var out map[string]any
	_, err := httpx.DoJSON(ctx, "GET", "https://chat.deepseek.com/api/v0/users/current", h, nil, &out, 15*time.Second)
	return out, err
}
