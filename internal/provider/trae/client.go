// Package trae 字节 TRAE SOLO（CN）上游客户端。
// 协议移植自 trae2api-web（开源，MIT）：Cloud-IDE-JWT 鉴权 + SOLO 自定义 SSE。
package trae

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/localimport"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

const (
	ClientID        = "en1oxy7wnw8j9n" // SOLO stable
	AppID           = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
	IdeVersion      = "0.1.52"
	IdeVersionCode  = "20260811"
	DeviceBrand     = "83DG"
	OSVersion       = "Windows 11 Pro"
	Function        = "solo_work_lite"
	DefaultModel    = "glm-5.2"
	clientUA        = "Trae/" + IdeVersion
	epChat          = "/api/agent/v3/llm_utils_chat"
	epModels        = "/api/ide/v1/get_detail_param"
	epExchange      = "/cloudide/api/v3/trae/oauth/ExchangeToken"
	epUserInfo      = "/cloudide/api/v3/trae/GetUserInfo"
	epCheckinStatus = "/trae/api/v2/ug/checkin_credits/status"
	epCheckinClaim  = "/trae/api/v2/ug/checkin_credits/claim"
	epEntUsage      = "/trae/api/v2/pay/ide_user_ent_usage"
)

func agentBase(pc *config.ProviderConfig) string {
	if pc != nil && pc.AgentBase != "" {
		return strings.TrimRight(pc.AgentBase, "/")
	}
	return "https://trae-api-cn.mchost.guru"
}

func oauthBase(pc *config.ProviderConfig, override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if pc != nil && pc.APIBase != "" {
		return strings.TrimRight(pc.APIBase, "/")
	}
	return "https://api.trae.com.cn"
}

func ugBase(pc *config.ProviderConfig) string {
	if pc != nil && pc.BillingBase != "" {
		return strings.TrimRight(pc.BillingBase, "/")
	}
	return "https://api.trae.cn"
}

// ── 请求头 ──

func soloHeaders(a *store.Account) map[string]string {
	h := map[string]string{
		"Content-Type":          "application/json",
		"Accept":                "text/event-stream",
		"User-Agent":            clientUA,
		"X-Cloudide-Token":      a.AccessToken,
		"X-Ide-Token":           a.AccessToken,
		"X-App-Id":              AppID,
		"X-App-Version":         "default",
		"X-Ide-Version":         IdeVersion,
		"X-Ide-Version-Code":    IdeVersionCode,
		"X-App-Version-Code":    IdeVersionCode,
		"X-Ide-Version-Type":    "stable",
		"X-Device-Type":         "windows",
		"X-OS-Version":          OSVersion,
		"X-Device-Brand":        DeviceBrand,
		"Request-Traffic-Type":  "prod",
	}
	if a.AccessToken != "" {
		h["Authorization"] = "Cloud-IDE-JWT " + a.AccessToken
	}
	if a.UID != "" {
		h["X-Uid"] = a.UID
	}
	if mid := a.ExtraGet(store.ExtraTraeMachineID); mid != "" {
		h["X-Machine-Id"] = mid
	}
	if did := a.ExtraGet(store.ExtraTraeDeviceID); did != "" {
		h["X-Device-Id"] = did
	}
	return h
}

func ugHeaders(a *store.Account) map[string]string {
	h := map[string]string{
		"Content-Type":   "application/json",
		"Accept":         "application/json",
		"User-Agent":     clientUA,
		"Authorization":  "Cloud-IDE-JWT " + a.AccessToken,
		"X-User-Region":  "CN",
	}
	if did := a.ExtraGet(store.ExtraTraeDeviceID); did != "" {
		h["X-Device-Id"] = did
	}
	return h
}

func oauthHeaders() map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"User-Agent":   clientUA,
	}
}

// Provider 实现 provider.Provider（Trae 仅 CN 渠道）。
type Provider struct {
	cfg *config.Config

	mmu      sync.Mutex
	modelsAt time.Time
	models   []provider.ModelInfo
}

func New(cfg *config.Config) *Provider {
	// 连接预热：保持对话上游热连接，请求时免握手
	httpx.RegisterWarm(agentBase(cfg.PC(store.PTrae)))
	return &Provider{cfg: cfg}
}

// SyncFromLocal 从本机 Trae 客户端重新同步 accessToken（按 UID 匹配）。
// 本机导入的账号没有 refreshToken（导入时故意排除，避免轮换踢掉 IDE），
// 但 Trae IDE 自己会轮换 token —— 重读 storage.json 即可拿到最新值。
// 注意：绝不写 acct.RefreshToken，保持与导入时一致的保护策略。
func (p *Provider) SyncFromLocal(acct *store.Account) error {
	locals := localimport.TraeLocal()
	if len(locals) == 0 {
		return provider.Err(provider.KindAuth, 0,
			"本机 Trae 客户端未登录或无法读取 storage.json")
	}
	l := locals[0]
	if acct.UID != "" && l.UID != "" && l.UID != acct.UID {
		return provider.Err(provider.KindAuth, 0,
			"本机 Trae 当前登录的是另一个账号（%s），请在 Trae 里切换回该账号后重试", l.DisplayedName())
	}
	acct.AccessToken = l.AccessToken
	if l.ExpiresAt > 0 {
		acct.ExpiresAt = l.ExpiresAt
	}
	if did := l.ExtraGet(store.ExtraTraeDeviceID); did != "" {
		acct.ExtraSet(store.ExtraTraeDeviceID, did)
	}
	acct.Dead = false
	acct.DeadReason = ""
	return nil
}

func (p *Provider) Name() string { return store.PTrae }

// CompleteLogin 用 refreshToken 完成登录（换 token + 拉用户信息）并构建账号。
func (p *Provider) CompleteLogin(ctx context.Context, refreshToken, apiHost, machineID, deviceID string) (*store.Account, error) {
	token, newRefresh, exp, err := p.ExchangeToken(ctx, refreshToken, apiHost)
	if err != nil {
		return nil, err
	}
	if apiHost == "" {
		apiHost = "https://api.trae.com.cn"
	}
	acct := &store.Account{
		ID:           store.NewID(),
		Provider:     store.PTrae,
		Realm:        "cn",
		AccessToken:  token,
		RefreshToken: newRefresh,
		ExpiresAt:    exp,
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
	}
	acct.ExtraSet(store.ExtraTraeAPIHost, apiHost)
	if machineID != "" {
		acct.ExtraSet(store.ExtraTraeMachineID, machineID)
	}
	if deviceID != "" {
		acct.ExtraSet(store.ExtraTraeDeviceID, deviceID)
	}
	if uid, nick, ent, uerr := p.GetUserInfo(ctx, token, apiHost); uerr == nil {
		acct.UID, acct.Nickname, acct.EnterpriseID = uid, nick, ent
	}
	return acct, nil
}

// ── token 刷新（ExchangeToken 轮换） ──

func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	if strings.TrimSpace(acct.RefreshToken) == "" {
		return provider.Err(provider.KindAuth, 0, "无 refreshToken，需要重新登录")
	}
	pc := p.cfg.PC(store.PTrae)
	body := map[string]any{
		"ClientID":     ClientID,
		"RefreshToken": acct.RefreshToken,
		"ClientSecret": "-",
		"UserID":       "",
	}
	var out struct {
		Result struct {
			Token               string `json:"Token"`
			TokenExpireAt       int64  `json:"TokenExpireAt"`
			TokenExpireDuration int64  `json:"TokenExpireDuration"`
			RefreshToken        string `json:"RefreshToken"`
		} `json:"Result"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", oauthBase(pc, acct.ExtraGet(store.ExtraTraeAPIHost))+epExchange, oauthHeaders(), body, &out, 30*time.Second); err != nil {
		return err
	}
	if out.Result.Token == "" {
		return provider.Err(provider.KindAuth, 0, "刷新失败：上游未返回 token，需要重新登录")
	}
	acct.AccessToken = out.Result.Token
	if out.Result.RefreshToken != "" {
		acct.RefreshToken = out.Result.RefreshToken
	}
	if exp := normalizeExpire(out.Result.TokenExpireAt); exp > 0 {
		acct.ExpiresAt = exp
	} else if out.Result.TokenExpireDuration > 0 {
		acct.ExpiresAt = time.Now().Unix() + out.Result.TokenExpireDuration
	}
	acct.LastRefresh = time.Now().Unix()
	logx.Infof("trae", "账号 %s token 已刷新", acct.DisplayedName())
	return nil
}

func normalizeExpire(v int64) int64 {
	if v > 1e12 {
		return v / 1000
	}
	return v
}

// ── 额度 ──

func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	pc := p.cfg.PC(store.PTrae)
	var out struct {
		UserEntitlementPackList []struct {
			EntitlementBaseInfo struct {
				Quota struct {
					CreditsLimit float64 `json:"credits_limit"`
				} `json:"quota"`
			} `json:"entitlement_base_info"`
			Usage struct {
				CreditsAmount float64 `json:"credits_amount"`
			} `json:"usage"`
		} `json:"user_entitlement_pack_list"`
		IsCreditsBilling bool `json:"is_credits_billing"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epEntUsage, ugHeaders(acct), map[string]any{}, &out, 30*time.Second); err != nil {
		return nil, err
	}
	snap := &store.QuotaSnapshot{UpdatedAt: time.Now().Unix(), Source: "ent_usage", Unit: "积分"}
	for i, pk := range out.UserEntitlementPackList {
		limit := pk.EntitlementBaseInfo.Quota.CreditsLimit
		if limit <= 0 {
			continue
		}
		used := pk.Usage.CreditsAmount
		snap.Total += limit
		snap.Used += used
		snap.Remain += limit - used
		snap.Packs++
		snap.Parts = append(snap.Parts, store.QuotaPart{Name: fmt.Sprintf("权益包 %d", i+1), Total: limit, Used: used, Remain: limit - used})
	}
	return snap, nil
}

// ── 签到 ──

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return true }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	pc := p.cfg.PC(store.PTrae)
	var status struct {
		CheckedIn bool    `json:"checked_in"`
		Credits   float64 `json:"credits"`
		Enable    bool    `json:"enable"`
		Code      int64   `json:"code"`
		Message   string  `json:"message"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinStatus, ugHeaders(acct), map[string]any{}, &status, 30*time.Second); err != nil {
		return false, "", err
	}
	if status.CheckedIn {
		return true, "今日已签到", nil
	}
	if status.Enable == false && status.Code != 0 {
		return false, status.Message, fmt.Errorf("签到状态查询异常: %s", status.Message)
	}
	var claim struct {
		Code    int64   `json:"code"`
		Message string  `json:"message"`
		Credits float64 `json:"credits"`
		Data    struct {
			Credits float64 `json:"credits"`
			Message string  `json:"message"`
		} `json:"data"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinClaim, ugHeaders(acct), map[string]any{}, &claim, 30*time.Second); err != nil {
		return false, "", err
	}
	msg := claim.Message
	if msg == "" {
		msg = claim.Data.Message
	}
	credits := claim.Credits
	if credits == 0 {
		credits = claim.Data.Credits
	}
	if claim.Code != 0 && claim.Code != 200 {
		if strings.Contains(msg, "已签到") || strings.Contains(strings.ToLower(msg), "already") {
			return true, "今日已签到", nil
		}
		return false, msg, fmt.Errorf("签到失败(code=%d): %s", claim.Code, msg)
	}
	if credits > 0 {
		return false, fmt.Sprintf("签到成功 +%g 积分", credits), nil
	}
	if msg != "" {
		return false, "签到成功: "+msg, nil
	}
	return false, "签到成功", nil
}

// ── 模型 ──

var seedModels = []provider.ModelInfo{
	{ID: "glm-5.2"}, {ID: "glm-5-turbo"}, {ID: "glm-5"},
	{ID: "Doubao-Seed-2.1-Pro"}, {ID: "Doubao-Seed-2.1-Turbo"},
	{ID: "DeepSeek-V4-Pro"}, {ID: "DeepSeek-V4-Flash"},
	{ID: "kimi-k3"}, {ID: "kimi-k2.7-code"}, {ID: "kimi-k2.6"},
	{ID: "minimax-m3"}, {ID: "qwen-3.7-plus"},
}

func (p *Provider) SeedModels() []provider.ModelInfo { return seedModels }

func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	p.mmu.Lock()
	if p.models != nil && time.Since(p.modelsAt) < time.Hour {
		p.mmu.Unlock()
		return p.models, nil
	}
	p.mmu.Unlock()
	pc := p.cfg.PC(store.PTrae)
	body := map[string]any{
		"function":            Function,
		"config_names":        nil,
		"need_prompt":         false,
		"current_config_info": nil,
		"poly_prompt":         true,
		"mode_type":           nil,
		"agent_type":          nil,
	}
	var out struct {
		ConfigInfoList []struct {
			ConfigName    string `json:"config_name"`
			DisplayConfig struct {
				DisplayName string `json:"display_name"`
			} `json:"display_config"`
		} `json:"config_info_list"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", agentBase(pc)+epModels, soloHeaders(acct), body, &out, 30*time.Second); err != nil {
		return nil, err
	}
	var list []provider.ModelInfo
	for _, c := range out.ConfigInfoList {
		if c.ConfigName == "" {
			continue
		}
		list = append(list, provider.ModelInfo{ID: c.ConfigName, Name: c.DisplayConfig.DisplayName})
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "模型列表为空")
	}
	p.mmu.Lock()
	p.models = list
	p.modelsAt = time.Now()
	p.mmu.Unlock()
	return list, nil
}
