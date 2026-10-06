// Package workbuddy 腾讯 CodeBuddy / WorkBuddy（CN 与国际站）上游客户端。
// 协议移植自 workbuddy-openai-proxy 与 workbuddy2api-panel（均为开源实现）：
// OAuth 设备码登录，纯 Bearer + 身份头鉴权，对话上游即 OpenAI 格式 SSE。
package workbuddy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/localimport"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const userAgent = "CLI/2.63.2 CodeBuddy/2.63.2"

// site 按领域划分的上游站点。
type site struct {
	ChatBase    string
	BillingBase string
	Origin      string
	BillingPath string // get-user-resource / daily-checkin 前缀
}

func siteFor(realm string, pc *config.ProviderConfig) site {
	if realm == "global" {
		chat := "https://www.workbuddy.ai"
		if pc != nil && pc.APIBase != "" {
			chat = pc.APIBase
		}
		bill := "https://www.workbuddy.ai"
		if pc != nil && pc.BillingBase != "" {
			bill = pc.BillingBase
		}
		return site{ChatBase: chat, BillingBase: bill, Origin: chat, BillingPath: "/billing/meter"}
	}
	chat := "https://copilot.tencent.com"
	if pc != nil && pc.APIBase != "" {
		chat = pc.APIBase
	}
	bill := "https://www.codebuddy.cn"
	if pc != nil && pc.BillingBase != "" {
		bill = pc.BillingBase
	}
	return site{ChatBase: chat, BillingBase: bill, Origin: "https://www.codebuddy.cn", BillingPath: "/v2/billing/meter"}
}

// Provider 实现 provider.Provider。
type Provider struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Provider {
	// 连接预热：国内站点与国际站都保持热连接，请求时免握手
	httpx.RegisterWarm(siteFor("", cfg.PC(store.PWorkBuddy)).ChatBase, siteFor("global", cfg.PC(store.PWorkBuddy)).ChatBase)
	return &Provider{cfg: cfg}
}

// SyncFromLocal 从本机 CodeBuddy 客户端会话文件重新同步凭据（按 UID 匹配）。
// 本机导入的账号可能没有 refreshToken，客户端自己会轮换 token，
// 重读 .info 会话即可拿到最新 accessToken / refreshToken。
func (p *Provider) SyncFromLocal(acct *store.Account) error {
	for _, l := range localimport.WorkBuddyLocal() {
		if l.UID == "" || l.UID != acct.UID {
			continue
		}
		acct.AccessToken = l.AccessToken
		if l.RefreshToken != "" {
			acct.RefreshToken = l.RefreshToken
		}
		if l.ExpiresAt > 0 {
			acct.ExpiresAt = l.ExpiresAt
		}
		acct.Domain = l.Domain
		acct.EnterpriseID = l.EnterpriseID
		acct.Dead = false
		acct.DeadReason = ""
		return nil
	}
	return provider.Err(provider.KindAuth, 0,
		"本机 CodeBuddy 客户端未找到该账号的登录会话（请确认客户端仍登录着该账号）")
}

func (p *Provider) Name() string { return store.PWorkBuddy }

// ── 请求头 ──

func commonHeaders(s site) map[string]string {
	return map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "application/json, text/plain, */*",
		"X-Requested-With": "XMLHttpRequest",
		"Origin":           s.Origin,
		"Referer":          s.Origin + "/",
		"User-Agent":       userAgent,
	}
}

func identityHeaders(h map[string]string, a *store.Account) map[string]string {
	if a.AccessToken != "" {
		h["Authorization"] = "Bearer " + a.AccessToken
	} else {
		h["X-No-Authorization"] = "1"
	}
	if a.UID != "" {
		h["X-User-Id"] = a.UID
	} else {
		h["X-No-User-Id"] = "1"
	}
	if a.EnterpriseID != "" {
		h["X-Enterprise-Id"] = a.EnterpriseID
	} else {
		h["X-No-Enterprise-Id"] = "1"
	}
	if a.Domain != "" {
		h["X-Domain"] = a.Domain
	} else {
		h["X-No-Department-Info"] = "1"
	}
	return h
}

func chatHeaders(s site, a *store.Account) map[string]string {
	h := commonHeaders(s)
	h["Accept"] = "application/json, text/event-stream"
	h["X-Product"] = "SaaS"
	h["X-Request-ID"] = randHex(16)
	h["X-Request-Trace-Id"] = provider.NewUUID()
	return identityHeaders(h, a)
}

func billingHeaders(s site, a *store.Account) map[string]string {
	h := commonHeaders(s)
	if a.EnterpriseID != "" {
		h["X-Tenant-Id"] = a.EnterpriseID
	}
	return identityHeaders(h, a)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── 登录（OAuth 设备码） ──

// LoginStart 发起设备码登录，返回授权 URL 与 state。
func (p *Provider) LoginStart(realm string) (state, authURL string, err error) {
	s := siteFor(realm, p.cfg.PC(store.PWorkBuddy))
	h := commonHeaders(s)
	for _, k := range []string{"X-No-Authorization", "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info"} {
		h[k] = "1"
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			State    string `json:"state"`
			AuthURL  string `json:"authUrl"`
		} `json:"data"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = httpx.DoJSON(ctx, "POST", s.ChatBase+"/v2/plugin/auth/state?platform=CLI", h, map[string]any{}, &out, 30*time.Second)
	if err != nil {
		return "", "", err
	}
	if out.Code != 0 || out.Data.State == "" {
		return "", "", fmt.Errorf("发起登录失败: %s", out.Msg)
	}
	return out.Data.State, out.Data.AuthURL, nil
}

// LoginPoll 轮询设备码登录结果；done=false 表示用户尚未完成授权。
func (p *Provider) LoginPoll(realm, state string) (done bool, acct *store.Account, err error) {
	s := siteFor(realm, p.cfg.PC(store.PWorkBuddy))
	h := commonHeaders(s)
	for _, k := range []string{"X-No-Authorization", "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info"} {
		h[k] = "1"
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int64  `json:"expiresIn"`
			Domain       string `json:"domain"`
		} `json:"data"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := httpx.DoJSON(ctx, "GET", s.ChatBase+"/v2/plugin/auth/token?state="+state, h, nil, &out, 30*time.Second); err != nil {
		return false, nil, err
	}
	if out.Code != 0 || out.Data.AccessToken == "" {
		return false, nil, nil
	}
	acct = &store.Account{
		ID:           store.NewID(),
		Provider:     store.PWorkBuddy,
		Realm:        realm,
		AccessToken:  out.Data.AccessToken,
		RefreshToken: out.Data.RefreshToken,
		ExpiresAt:    time.Now().Unix() + out.Data.ExpiresIn,
		Domain:       out.Data.Domain,
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
	}
	hydrateFromJWT(acct)
	// 拉账号昵称
	var acc struct {
		Code int `json:"code"`
		Data struct {
			UID          string `json:"uid"`
			EnterpriseID string `json:"enterpriseId"`
			Nickname     string `json:"nickname"`
		} `json:"data"`
	}
	h2 := commonHeaders(s)
	h2["Authorization"] = "Bearer " + acct.AccessToken
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	if _, err := httpx.DoJSON(ctx2, "GET", s.ChatBase+"/v2/plugin/login/account?state="+state, h2, nil, &acc, 20*time.Second); err == nil && acc.Code == 0 {
		if acc.Data.UID != "" {
			acct.UID = acc.Data.UID
		}
		if acc.Data.EnterpriseID != "" {
			acct.EnterpriseID = acc.Data.EnterpriseID
		}
		acct.Nickname = acc.Data.Nickname
	}
	if acct.EnterpriseID == "" {
		acct.EnterpriseID = enterpriseFromJWT(acct.AccessToken)
	}
	return true, acct, nil
}

// ── JWT 工具 ──

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func enterpriseFromJWT(token string) string {
	m := jwtClaims(token)
	if m == nil {
		return ""
	}
	if iss, ok := m["iss"].(string); ok {
		if i := strings.LastIndex(iss, "/sso-"); i >= 0 {
			return iss[i+5:]
		}
	}
	return ""
}

func hydrateFromJWT(a *store.Account) {
	m := jwtClaims(a.AccessToken)
	if m == nil {
		return
	}
	if a.ExpiresAt <= 0 {
		if exp, ok := m["exp"].(float64); ok {
			a.ExpiresAt = int64(exp)
		}
	}
	if a.UID == "" {
		if sub, ok := m["sub"].(string); ok {
			a.UID = sub
		}
	}
	if a.EnterpriseID == "" {
		a.EnterpriseID = enterpriseFromJWT(a.AccessToken)
	}
}

// ── token 刷新 ──

func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	if strings.TrimSpace(acct.RefreshToken) == "" {
		return provider.Err(provider.KindAuth, 0, "无 refreshToken，需要重新登录")
	}
	s := siteFor(acct.Realm, p.cfg.PC(store.PWorkBuddy))
	h := commonHeaders(s)
	h["X-Refresh-Token"] = acct.RefreshToken
	h["X-Auth-Refresh-Source"] = "workbuddy"
	if acct.EnterpriseID != "" {
		h["X-Enterprise-Id"] = acct.EnterpriseID
	}
	var out struct {
		Code int `json:"code"`
		Data struct {
			AccessToken string `json:"accessToken"`
			ExpiresIn   int64  `json:"expiresIn"`
		} `json:"data"`
		Msg string `json:"msg"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", s.ChatBase+"/v2/plugin/auth/token/refresh", h, "", &out, 30*time.Second); err != nil {
		return err
	}
	if out.Code != 0 || out.Data.AccessToken == "" {
		return provider.Err(provider.KindAuth, 0, "刷新失败: %s", out.Msg)
	}
	acct.AccessToken = out.Data.AccessToken
	if out.Data.ExpiresIn > 0 {
		acct.ExpiresAt = time.Now().Unix() + out.Data.ExpiresIn
	} else {
		hydrateFromJWT(acct)
	}
	acct.LastRefresh = time.Now().Unix()
	logx.Infof("workbuddy", "账号 %s token 已刷新", acct.DisplayedName())
	return nil
}

// ── 额度 ──

func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	s := siteFor(acct.Realm, p.cfg.PC(store.PWorkBuddy))
	now := time.Now()
	body := map[string]any{
		"PageNumber":                 1,
		"PageSize":                   100,
		"ProductCode":                "p_tcaca",
		"Status":                     []int{0, 3},
		"PackageEndTimeRangeBegin":   now.Format("2006-01-02 15:04:05"),
		"PackageEndTimeRangeEnd":     now.Add(365 * 101 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	}
	h := billingHeaders(s, acct)
	paths := []string{s.BillingPath + "/get-user-resource"}
	if acct.Realm == "global" {
		paths = append(paths, "/v2/billing/meter/get-user-resource")
	}
	var lastErr error
	for _, path := range paths {
		var out struct {
			Code int `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Response struct {
					Data struct {
						Accounts []struct {
							PackageName         string  `json:"PackageName"`
							CapacityRemain      float64 `json:"CapacityRemain"`
							CapacityUsed        float64 `json:"CapacityUsed"`
							CapacitySize        float64 `json:"CapacitySize"`
							CycleCapacityRemain float64 `json:"CycleCapacityRemain"`
							CycleCapacityUsed   float64 `json:"CycleCapacityUsed"`
							CycleCapacitySize   float64 `json:"CycleCapacitySize"`
						} `json:"Accounts"`
					} `json:"Data"`
				} `json:"Response"`
			} `json:"data"`
		}
		st, err := httpx.DoJSON(ctx, "POST", s.BillingBase+path, h, body, &out, 30*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		if st >= 400 {
			lastErr = provider.ClassifyBody(st, nil)
			continue
		}
		if out.Code != 0 {
			lastErr = fmt.Errorf("code=%d %s", out.Code, out.Msg)
			continue
		}
		snap := &store.QuotaSnapshot{UpdatedAt: time.Now().Unix(), Source: "billing"}
		for _, a := range out.Data.Response.Data.Accounts {
			var remain, used, size float64
			if a.CycleCapacitySize > 0 {
				remain, used, size = a.CycleCapacityRemain, a.CycleCapacityUsed, a.CycleCapacitySize
			} else {
				remain, used, size = a.CapacityRemain, a.CapacityUsed, a.CapacitySize
			}
			snap.Remain += remain
			snap.Used += used
			snap.Total += size
			snap.Packs++
			name := a.PackageName
			if name == "" {
				name = fmt.Sprintf("package-%d", snap.Packs)
			}
			snap.Parts = append(snap.Parts, store.QuotaPart{Name: name, Total: size, Used: used, Remain: remain})
		}
		snap.Unit = "积分"
		return snap, nil
	}
	return nil, lastErr
}

// ── 签到 ──

func (p *Provider) SupportsCheckin(acct *store.Account) bool {
	if acct.Realm != "global" {
		return true
	}
	return p.cfg.WBIntlKeepalive
}

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	s := siteFor(acct.Realm, p.cfg.PC(store.PWorkBuddy))
	if acct.Realm == "global" {
		if !p.cfg.WBIntlKeepalive {
			return false, "", provider.ErrCheckinUnsupported
		}
		return p.keepalive(ctx, acct, s)
	}
	h := billingHeaders(s, acct)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", s.BillingBase+s.BillingPath+"/daily-checkin", h, map[string]any{}, &out, 30*time.Second); err != nil {
		return false, "", err
	}
	if out.Code == 0 {
		return false, "签到成功", nil
	}
	if out.Code == 10001 || strings.Contains(out.Msg, "已签到") || strings.Contains(strings.ToLower(out.Msg), "already") {
		return true, "今日已签到", nil
	}
	return false, out.Msg, fmt.Errorf("签到失败: %s", out.Msg)
}

// keepalive 国际版无签到：发一条最小对话保持账号活跃（移植自 CreditDaddy）。
func (p *Provider) keepalive(ctx context.Context, acct *store.Account, s site) (bool, string, error) {
	body := map[string]any{
		"model":      "hy4-preview",
		"stream":     true,
		"max_tokens": 16,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are CodeBuddy Code."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := httpx.DoStream(ctx, "POST", s.ChatBase+"/v2/chat/completions", chatHeaders(s, acct), raw, 30*time.Second)
	if err != nil {
		return false, "", err
	}
	defer func() {
		if resp.Stream != nil {
			resp.Stream.Close()
		}
	}()
	if resp.Status >= 400 {
		return false, "", provider.ClassifyBody(resp.Status, resp.Body)
	}
	// 读一小段就算成功
	buf := make([]byte, 512)
	_, _ = resp.Stream.Read(buf)
	return false, "国际版保活完成", nil
}
