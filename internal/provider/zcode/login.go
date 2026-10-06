package zcode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/store"
)

const (
	APIBase   = "https://zcode.z.ai"
	BigmodelBase = "https://open.bigmodel.cn"
	zaiBusinessLoginURL = "https://api.z.ai/api/auth/z/login"
)

// LoginSession 一次 CLI 轮询登录会话。
type LoginSession struct {
	Provider   string `json:"provider"` // bigmodel | zai
	FlowID     string `json:"flowId"`
	PollToken  string `json:"-"`
	AuthorizeURL string `json:"authorizeUrl"`
	IntervalMS int    `json:"intervalMs"`
	ExpiresAt  int64  `json:"expiresAt"`
}

// LoginStart 与 ZCode 客户端「CLI 轮询登录」同一协议。
func LoginStart(providerKind string) (*LoginSession, error) {
	p := providerKind
	if p != "zai" && p != "bigmodel" {
		p = "bigmodel"
	}
	pollToken := strings.ToLower(store.NewID()) + strings.ToLower(store.NewID())
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			FlowID      string `json:"flow_id"`
			AuthorizeURL string `json:"authorize_url"`
			ExpiresAt   float64 `json:"expires_at"`
			PollIntervalSec int64 `json:"poll_interval_sec"`
		} `json:"data"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hdr := map[string]string{
		"Authorization": "Bearer " + pollToken,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	_, err := httpx.DoJSON(ctx, "POST", APIBase+"/api/v1/oauth/cli/init", hdr, map[string]any{"provider": p}, &out, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if out.Code != 0 || out.Data.FlowID == "" || out.Data.AuthorizeURL == "" {
		return nil, fmt.Errorf("ZCode 登录初始化失败: %s", out.Msg)
	}
	if !strings.HasPrefix(out.Data.AuthorizeURL, "https://") {
		return nil, fmt.Errorf("ZCode 登录初始化返回了非 https 授权地址")
	}
	expiresIn := int64(5 * 60)
	if out.Data.ExpiresAt > 0 {
		expiresIn = int64(out.Data.ExpiresAt) - time.Now().Unix()
		if expiresIn < 60 {
			expiresIn = 60
		}
		if expiresIn > 15*60 {
			expiresIn = 15 * 60
		}
	}
	interval := out.Data.PollIntervalSec
	if interval < 1 {
		interval = 2
	}
	return &LoginSession{
		Provider:    p,
		FlowID:      out.Data.FlowID,
		PollToken:   pollToken,
		AuthorizeURL: out.Data.AuthorizeURL,
		IntervalMS:  int(interval) * 1000,
		ExpiresAt:   time.Now().Unix() + expiresIn,
	}, nil
}

// LoginPollResult 轮询结果。
type LoginPollResult struct {
	Status string          `json:"status"` // pending | ok | failed
	Error  string          `json:"error,omitempty"`
	Account *store.Account `json:"-"`
}

// LoginPoll 轮询一次登录状态；ready 时构建账号。
func LoginPoll(ctx context.Context, sess *LoginSession) (*LoginPollResult, error) {
	hdr := map[string]string{
		"Authorization": "Bearer " + sess.PollToken,
		"Accept":        "application/json",
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Status string `json:"status"`
			Token  string `json:"token"`
			User   map[string]any `json:"user"`
			Bigmodel map[string]any `json:"bigmodel"`
			Zai    map[string]any `json:"zai"`
		} `json:"data"`
	}
	_, err := httpx.DoJSON(ctx, "GET", APIBase+"/api/v1/oauth/cli/poll/"+sess.FlowID, hdr, nil, &out, 30*time.Second)
	if err != nil {
		return &LoginPollResult{Status: "pending"}, nil
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("ZCode 授权失败: %s", out.Msg)
	}
	switch out.Data.Status {
	case "pending":
		return &LoginPollResult{Status: "pending"}, nil
	case "failed":
		return nil, fmt.Errorf("ZCode 授权失败（用户取消或授权被拒绝）")
	case "ready":
	default:
		return nil, fmt.Errorf("ZCode 授权返回未知状态: %s", out.Data.Status)
	}
	jwt := strings.TrimSpace(out.Data.Token)
	if jwt == "" {
		return nil, fmt.Errorf("ZCode 授权返回缺少 token（上游结构可能变化）")
	}
	acct := &store.Account{
		ID:          store.NewID(),
		Provider:    store.PZCode,
		Realm:       sess.Provider,
		AccessToken: jwt,
		Enabled:     true,
		CreatedAt:   time.Now().Unix(),
	}
	acct.ExtraSet(store.ExtraZCodeLoginProvider, sess.Provider)
	if u := out.Data.User; u != nil {
		if v, ok := u["user_id"]; ok && v != nil {
			acct.UID = fmt.Sprintf("%v", v)
		}
		if name, _ := u["name"].(string); name != "" {
			acct.Nickname = name
		}
		if email, _ := u["email"].(string); email != "" {
			acct.Email = email
		}
	}
	if m := DecodeJWT(jwt); m != nil {
		if acct.UID == "" {
			if v, ok := m["user_id"]; ok {
				acct.UID = fmt.Sprintf("%v", v)
			}
		}
	}
	switch sess.Provider {
	case "zai":
		if at, _ := out.Data.Zai["access_token"].(string); at != "" {
			at, err := resolveZaiBusinessToken(ctx, at)
			if err != nil {
				return nil, err
			}
			acct.ExtraSet(store.ExtraZCodeZaiAT, at)
		}
	default:
		if at, _ := out.Data.Bigmodel["access_token"].(string); at != "" {
			acct.ExtraSet(store.ExtraZCodeBigmodelAT, at)
		}
		if rt, _ := out.Data.Bigmodel["refresh_token"].(string); rt != "" {
			acct.ExtraSet(store.ExtraZCodeBigmodelRT, rt)
		}
	}
	if acct.Nickname == "" {
		acct.Nickname = "ZCode " + sess.Provider
	}
	return &LoginPollResult{Status: "ok", Account: acct}, nil
}

// resolveZaiBusinessToken Z.ai OAuth token → 业务 token。
func resolveZaiBusinessToken(ctx context.Context, oauthToken string) (string, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data map[string]any `json:"data"`
	}
	hdr := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if _, err := httpx.DoJSON(ctx, "POST", zaiBusinessLoginURL, hdr, map[string]any{"token": oauthToken}, &out, 30*time.Second); err != nil {
		return "", err
	}
	if t, _ := out.Data["access_token"].(string); t != "" {
		return t, nil
	}
	if t, _ := out.Data["accessToken"].(string); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("Z.ai 业务 token 交换失败: %s", out.Msg)
}
