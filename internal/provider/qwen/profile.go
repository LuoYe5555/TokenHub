// 千问令牌续期：
// 1) refresh_token 换新 access token（/api/v2/auths/refresh，对齐 qwengate）
// 2) 失效时无头模式从浏览器配置目录重读 refresh cookie 再换
// （一键导入时用户登录过一次，refresh cookie 会长期留在 data/qwen-profile）。
package qwen

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"tokenhub/internal/provider"
	"tokenhub/internal/qwencdp"
	"tokenhub/internal/store"
)

// profileDir 与 web 侧 qwenbrowser.go 保持一致。
const profileDir = "data/qwen-profile"

// exchangeAcct 用账号的 refreshToken 换新 accessToken 并写回；成功返回 true。
func (p *Provider) exchangeAcct(ctx context.Context, acct *store.Account) bool {
	res, err := ExchangeRefresh(ctx, p.base(), acct.RefreshToken)
	if err != nil || res.Token == "" {
		return false
	}
	// 新令牌先验证再写入，避免把坏值落库
	cand := *acct
	cand.AccessToken = res.Token
	if _, err := p.fetchUser(ctx, &cand); err != nil {
		return false
	}
	acct.AccessToken = res.Token
	if res.RefreshToken != "" {
		acct.RefreshToken = res.RefreshToken
	}
	return true
}

// refreshFromProfile 无头打开浏览器配置目录，重读最新 refresh cookie 后换取。
func (p *Provider) refreshFromProfile(ctx context.Context, acct *store.Account) bool {
	profile, _ := filepath.Abs(profileDir)
	if !qwencdp.ProfileHasCookies(profile) {
		return false
	}
	exe := qwencdp.FindBrowser()
	if exe == "" {
		return false
	}
	port, err := qwencdp.FreePort()
	if err != nil {
		return false
	}
	cmd, err := qwencdp.Launch(exe, profile, port, p.base()+"/", true)
	if err != nil {
		return false
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	if !qwencdp.WaitDebug(port, 20*time.Second) {
		return false
	}
	// 让站点真正加载一次（refresh cookie 可能被前端轮换），给足加载时间
	time.Sleep(12 * time.Second)
	cookies, err := qwencdp.GetCookies(port)
	if err != nil {
		return false
	}
	// 顺手更新风控 cookie 与签名（bx-ua 等由站点 JS 生成，抓真实请求头复用）
	if wc := qwencdp.CookieHeader(cookies, "chat.qwen.ai"); wc != "" {
		if acct.Extra == nil {
			acct.Extra = map[string]string{}
		}
		acct.Extra["wafCookie"] = wc
	}
	if h := qwencdp.SniffAPIHeaders(port, "chat.qwen.ai", 8*time.Second, true); len(h) > 0 {
		if acct.Extra == nil {
			acct.Extra = map[string]string{}
		}
		for _, pair := range [][2]string{{"bx-ua", "bxUa"}, {"bx-umidtoken", "bxUmid"}, {"bx-v", "bxV"}, {"version", "siteVer"}} {
			if v := h[pair[0]]; v != "" {
				acct.Extra[pair[1]] = v
			}
		}
	}
	token, refresh := qwencdp.PickAuth(cookies)
	if token == "" && refresh == "" {
		return false
	}
	// 旧式 token cookie 直接验
	if token != "" && token != acct.AccessToken {
		cand := *acct
		cand.AccessToken = token
		if _, err := p.fetchUser(ctx, &cand); err == nil {
			acct.AccessToken = token
			if refresh != "" {
				acct.RefreshToken = refresh
			}
			return true
		}
	}
	// 新版流程：浏览器里的 refresh cookie（可能比账号里的新）去换
	if refresh != "" {
		saved := acct.RefreshToken
		if refresh != saved {
			acct.RefreshToken = refresh
		}
		if p.exchangeAcct(ctx, acct) {
			return true
		}
		acct.RefreshToken = saved
	}
	return false
}

// Refresh 校验令牌；失效时依次尝试 refresh 换取、浏览器配置目录续期。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	_, err := p.fetchUser(ctx, acct)
	if err == nil {
		return nil
	}
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Kind != provider.KindAuth {
		return err // 网络/服务端问题不走本地续期
	}
	if p.exchangeAcct(ctx, acct) {
		return nil
	}
	if p.refreshFromProfile(ctx, acct) {
		return nil
	}
	return err
}
