// DeepSeek 令牌保活：常驻一个隐藏 CDP 浏览器（与一键导入共用
// data/deepseek-profile 配置目录与浏览器状态），定期读取页面
// localStorage.userToken 并同步进账号——网页前端轮换令牌时自动跟平，
// 实现与千问一致的「凭据自动续期」体验，基本免去手动重新导入。
//
// 与一键导入的关系：导入窗口（headed）打开期间保活直接复用该窗口的
// 调试端口；窗口被关掉后保活自动拉起隐藏（headless）实例顶上；
// 用户点「一键导入」时 dsBrStopLocked 会杀掉保活的隐藏实例，让位给导入窗口。
package web

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/logx"
	"tokenhub/internal/qwencdp"
	"tokenhub/internal/store"
)

var dsKeepLog atomic.Value    // 节流日志时间戳
var dsKeepReopen atomic.Value // 上次自动弹窗时间（1 小时冷却）

func init() {
	dsKeepLog.Store(time.Time{})
	dsKeepReopen.Store(time.Time{})
}

// dsKeepThrottledLog 保活日志节流：同一条 10 分钟最多记一次，避免轮询刷屏。
func dsKeepThrottledLog(msg string) {
	if time.Since(dsKeepLog.Load().(time.Time)) > 10*time.Minute {
		dsKeepLog.Store(time.Now())
		logx.Infof("panel", "%s", msg)
	}
}

// StartDSKeepAlive 启动保活后台循环（非阻塞，进程内单实例）。
func StartDSKeepAlive(cfg *config.Config, st *store.Store) {
	go func() {
		time.Sleep(15 * time.Second) // 等启动高峰（本机自动导入等）过去
		failStreak := 0
		for {
			if !cfg.DsKeepAliveOn() {
				time.Sleep(15 * time.Second)
				continue
			}
			var acct *store.Account
			for _, a := range st.All() {
				if a.Provider == store.PDeepSeek {
					acct = a
					break
				}
			}
			if acct == nil {
				time.Sleep(time.Minute)
				continue
			}
			port := dsKeepAlivePort()
			if port == 0 {
				port, lerr := dsKeepLaunch()
				if lerr != nil {
					dsKeepThrottledLog(fmt.Sprintf("DeepSeek 保活：浏览器启动失败（5 分钟后重试）: %v", lerr))
					time.Sleep(5 * time.Minute)
					continue
				}
				logx.Infof("panel", "DeepSeek 保活：隐藏浏览器已启动（port=%d），开始同步网页端令牌", port)
				time.Sleep(5 * time.Second) // 等页面加载
			}
			raw, err := qwencdp.GetLocalStorageValue(port, dsHostPart, dsSiteURL, "userToken", 8*time.Second)
			if err != nil {
				failStreak++
				if failStreak >= 3 {
					// 窗口可能已被用户手动关掉或崩了：清掉旧实例重拉
					dsBr.mu.Lock()
					dsBrStopLocked()
					dsBr.mu.Unlock()
					failStreak = 0
					dsKeepThrottledLog("DeepSeek 保活：页面读取连续失败，已重启隐藏浏览器")
				}
				time.Sleep(90 * time.Second)
				continue
			}
			failStreak = 0
			token := dsParseUserToken(raw)
			if token == "" {
				if strings.Contains(raw, `"value":null`) {
					// 区分两种情况：账号令牌仍有效（页面登录态镜像暂未同步，
					// 继续观察）vs 凭据已被服务端作废（单会话互踢/TTL）——
					// 后者自动弹出登录窗口，用户输一次验证码即恢复，其余全自动
					if dsAccountTokenValid(acct) {
						dsKeepThrottledLog("DeepSeek 保活：页面登录态暂未同步，但账号令牌经上游校验仍有效，继续观察")
					} else {
						dsKeepAutoReopen()
					}
				}
				time.Sleep(2 * time.Minute)
				continue
			}
			syncDSToken(st, port, token)
			time.Sleep(time.Minute)
		}
	}()
}

// dsAccountTokenValid 上游校验账号当前令牌，用于区分「页面登录态镜像失真」
// 与「凭据已被服务端真正作废」。
func dsAccountTokenValid(acct *store.Account) bool {
	if acct == nil || acct.AccessToken == "" {
		return false
	}
	cookie := ""
	if acct.Extra != nil {
		cookie = acct.Extra["dsCookie"]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user, err := dsFetchUser(ctx, acct.AccessToken, cookie)
	if err != nil {
		return false
	}
	code, _ := user["code"].(float64)
	return code == 0 || code == 200
}

// dsKeepAutoReopen 凭据确认失效后自动弹出 headed 登录窗口（1 小时冷却，
// 避免无人值守时反复弹窗）。用户在窗口里完成验证码登录后，页面写入新令牌，
// 保活循环下一轮自动同步进账号并解除冷却/死亡态——恢复全程无需其他操作。
func dsKeepAutoReopen() {
	if t, ok := dsKeepReopen.Load().(time.Time); ok && time.Since(t) < time.Hour {
		dsKeepThrottledLog("DeepSeek 保活：凭据已失效，等待在已打开的登录窗口里完成登录")
		return
	}
	dsKeepReopen.Store(time.Now())
	if _, err := dsStartImportBrowser(); err != nil {
		logx.Infof("panel", "DeepSeek 保活：凭据已失效，自动弹出登录窗口失败: %v（可手动点面板「一键浏览器导入」）", err)
		return
	}
	logx.Infof("panel", "DeepSeek 凭据已被服务端作废：已自动弹出登录窗口，请在窗口里完成验证码登录，登录后令牌会自动同步恢复")
}

// dsKeepAlivePort 返回当前保活/导入共用浏览器的调试端口（0=没有在跑的实例）。
func dsKeepAlivePort() int {
	dsBr.mu.Lock()
	defer dsBr.mu.Unlock()
	return dsBr.port
}

// dsKeepLaunch 拉起隐藏浏览器实例（headless，登录态来自共用配置目录）。
// 返回调试端口；失败返回带原因的 error。
func dsKeepLaunch() (int, error) {
	dsBr.mu.Lock()
	defer dsBr.mu.Unlock()
	exe := qwencdp.FindBrowser()
	if exe == "" {
		return 0, errors.New("未找到可用的 Edge/Chrome")
	}
	port, err := qwencdp.FreePort()
	if err != nil {
		return 0, fmt.Errorf("取空闲端口失败: %w", err)
	}
	profile, _ := filepath.Abs(dsProfileDir)
	cmd, err := qwencdp.Launch(exe, profile, port, dsSiteURL, true)
	if err != nil {
		return 0, fmt.Errorf("启动浏览器失败: %w", err)
	}
	dsBr.port = port
	if cmd.Process != nil {
		dsBr.proc = cmd.Process
	}
	if !qwencdp.WaitDebug(port, 25*time.Second) {
		return 0, fmt.Errorf("调试端口 %d 等待超时（浏览器可能被配置目录锁占用）", port)
	}
	return port, nil
}

// syncDSToken 把保活页面读到的令牌同步进账号。
// 防呆：仅当页面令牌经上游校验有效才覆盖——防止把「用户在别的浏览器重新登录后、
// 保活页残留的死令牌」写进账号把好凭据顶掉；反向场景（账号死了、页面令牌有效）
// 则自动救活并清冷却。
func syncDSToken(st *store.Store, port int, token string) {
	for _, a := range st.All() {
		if a.Provider == store.PDeepSeek && a.AccessToken == token {
			return // 已是最新，无事
		}
	}
	cookie := ""
	if cookies, err := qwencdp.GetCookies(port); err == nil {
		cookie = qwencdp.CookieHeader(cookies, dsHostPart)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user, err := dsFetchUser(ctx, token, cookie)
	valid := false
	if err == nil {
		if code, _ := user["code"].(float64); code == 0 || code == 200 {
			valid = true
		}
	}
	if !valid {
		dsKeepThrottledLog("DeepSeek 保活：页面令牌未通过上游校验（可能在别处重新登录导致旧会话作废），跳过同步")
		return
	}
	// 更新与令牌匹配的账号；没有匹配则更新第一个 DeepSeek 账号（等同重新导入修复）
	targetID := ""
	for _, a := range st.All() {
		if a.Provider != store.PDeepSeek {
			continue
		}
		if targetID == "" {
			targetID = a.ID
		}
		if a.AccessToken == token {
			targetID = a.ID
			break
		}
	}
	if targetID == "" {
		return
	}
	_ = st.Mutate(targetID, func(a *store.Account) {
		a.AccessToken = token
		dsSetCookie(a, cookie)
		// 令牌刷新成功视为凭据修复：清掉死亡/冷却状态
		a.Dead = false
		a.DeadReason = ""
		a.CooldownUntil = 0
		a.CooldownReason = ""
	})
	logx.Infof("panel", "DeepSeek 保活：令牌已同步网页端最新值（len=%d）", len(token))
}
