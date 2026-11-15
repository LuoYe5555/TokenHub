// Package trae 字节 TRAE SOLO（CN）上游客户端。
// 协议移植自 trae2api-web（开源，MIT）：Cloud-IDE-JWT 鉴权 + SOLO 自定义 SSE。
package trae

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
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
		"Content-Type":         "application/json",
		"Accept":               "text/event-stream",
		"User-Agent":           clientUA,
		"X-Cloudide-Token":     a.AccessToken,
		"X-Ide-Token":          a.AccessToken,
		"X-App-Id":             AppID,
		"X-App-Version":        "default",
		"X-Ide-Version":        IdeVersion,
		"X-Ide-Version-Code":   IdeVersionCode,
		"X-App-Version-Code":   IdeVersionCode,
		"X-Ide-Version-Type":   "stable",
		"X-Device-Type":        "windows",
		"X-OS-Version":         OSVersion,
		"X-Device-Brand":       DeviceBrand,
		"Request-Traffic-Type": "prod",
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
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"User-Agent":    clientUA,
		"Authorization": "Cloud-IDE-JWT " + a.AccessToken,
		"X-User-Region": "CN",
		"X-Device-Type": "Windows",
		"X-OS-Version":  "10.0.19045",
		"X-App-Version": IdeVersion,
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
	// 注意：不复制本机设备号——账号的 deviceId 是导入时随机生成的独立指纹，
	// 被风控拉黑也只烧随机号，绝不牵连本机 IDE 的真实机器码。
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
	ensureDeviceID(acct) // 登录流未捕获到设备号时补一个（签到/下单接口必需）
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

// genDeviceID 生成 16 位数字 deviceId（与真实客户端同格式，44 开头）。
// 签到/下单接口需要 X-Device-Id，缺失时上游报 9004"下单参数错误"。
func genDeviceID() string {
	b := make([]byte, 14)
	for i := range b {
		b[i] = byte('0' + rand.Intn(10))
	}
	return "44" + string(b)
}

// ensureDeviceID 给缺 deviceId 的账号补一个（幂等；调用方负责持久化）。
func ensureDeviceID(a *store.Account) {
	if strings.TrimSpace(a.ExtraGet(store.ExtraTraeDeviceID)) == "" {
		a.ExtraSet(store.ExtraTraeDeviceID, genDeviceID())
	}
}

// RawCheckinSources 签到相关上游接口的原始响应（面板/探针诊断用）。
func (p *Provider) RawCheckinSources(ctx context.Context, acct *store.Account) map[string]any {
	pc := p.cfg.PC(store.PTrae)
	out := map[string]any{}
	get := func(key, url string) {
		var v any
		if _, err := httpx.DoJSON(ctx, "POST", url, ugHeaders(acct), checkinBody, &v, 30*time.Second); err != nil {
			out[key] = map[string]any{"error": err.Error()}
		} else {
			out[key] = v
		}
	}
	get("checkin_status", ugBase(pc)+epCheckinStatus)
	return out
}

// DebugCheckinClaim 实际执行一次签到领取并返回原始响应（诊断 9004 之类用）。
func (p *Provider) DebugCheckinClaim(ctx context.Context, acct *store.Account) (map[string]any, error) {
	pc := p.cfg.PC(store.PTrae)
	var raw map[string]any
	if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinClaim, ugHeaders(acct), checkinBody, &raw, 30*time.Second); err != nil {
		return nil, err
	}
	return raw, nil
}

// checkinBody 与桌面端一致（req_source=1）。
// 桌面端实测逆向确认：status/claim 空 body 会被上游拒成 9074"当前参与用户太多"。
var checkinBody = map[string]any{"req_source": 1}

// Checkin 每账号每天一次（上游按账号去重，换设备号也不多加额度）。
func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	ensureDeviceID(acct) // 缺失时补（9004 根因）；调用方随后会落库
	pc := p.cfg.PC(store.PTrae)
	var status struct {
		CheckedIn bool    `json:"checked_in"`
		Credits   float64 `json:"credits"`
		Enable    bool    `json:"enable"`
		Code      int64   `json:"code"`
		Message   string  `json:"message"`
	}
	if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinStatus, ugHeaders(acct), checkinBody, &status, 30*time.Second); err != nil {
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
	if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinClaim, ugHeaders(acct), checkinBody, &claim, 30*time.Second); err != nil {
		return false, "", err
	}
	// 9074 排队限流：梯度退避原地重试（8s→15s→25s），通常一两轮就能挤过去
	for attempt, wait := 0, 8*time.Second; claim.Code == 9074 && attempt < 3; attempt++ {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return false, "", ctx.Err()
		}
		wait += 7 * time.Second
		if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinClaim, ugHeaders(acct), checkinBody, &claim, 30*time.Second); err != nil {
			return false, "", err
		}
	}
	// 9074 退避重试仍失败：设备号可能已被上游拉黑（实测：status 正常而 claim
	// 恒 9074，换全新设备号立即成功——多设备实验期触发过风控的账号会这样）。
	// 换一个新设备号再试一轮；成功则新号落库（调用方负责持久化），仍失败则还原。
	if claim.Code == 9074 {
		oldDev := acct.ExtraGet(store.ExtraTraeDeviceID)
		acct.ExtraSet(store.ExtraTraeDeviceID, genDeviceID())
		h := ugHeaders(acct)
		for attempt, wait := 0, 10*time.Second; claim.Code == 9074 && attempt < 2; attempt++ {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return false, "", ctx.Err()
			}
			wait += 10 * time.Second
			if _, err := httpx.DoJSON(ctx, "POST", ugBase(pc)+epCheckinClaim, h, checkinBody, &claim, 30*time.Second); err != nil {
				acct.ExtraSet(store.ExtraTraeDeviceID, oldDev)
				return false, "", err
			}
		}
		if claim.Code == 9074 {
			acct.ExtraSet(store.ExtraTraeDeviceID, oldDev)
			return false, "签到排队限流(9074)，重试+换设备号仍被限流，稍后会自动补签", fmt.Errorf("签到失败(code=9074): 当前参与用户太多，多次自动重试（含换设备号）仍被限流，等下一轮自动补签")
		}
	}
	msg := claim.Message
	if msg == "" {
		msg = claim.Data.Message
	}
	credits := claim.Credits
	if credits == 0 {
		credits = claim.Data.Credits
	}
	if claim.Code == 9095 || (claim.Code != 0 && claim.Code != 200 && (strings.Contains(msg, "已签到") || strings.Contains(strings.ToLower(msg), "already"))) {
		return true, "今日已签到", nil
	}
	if claim.Code != 0 && claim.Code != 200 {
		return false, msg, fmt.Errorf("签到失败(code=%d): %s", claim.Code, msg)
	}
	if credits > 0 {
		return false, fmt.Sprintf("签到成功 +%g 积分", credits), nil
	}
	if msg != "" {
		return false, "签到成功: " + msg, nil
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

// tagIn 判断 tags 里是否已存在指定文案。
func tagIn(tags []string, text string) bool {
	for _, t := range tags {
		if part := strings.SplitN(t, "|", 2)[0]; part == text {
			return true
		}
	}
	return false
}

// contactConfig 是 display_contact_config（JSON 字符串）里我们关心的部分：
// consumption_rate=计费倍率，activity_discount=补贴/限时优惠活动。
type contactConfig struct {
	ConsumptionRate struct {
		Enable bool `json:"enable"`
		Data   struct {
			Rate float64 `json:"rate"`
		} `json:"data"`
	} `json:"consumption_rate"`
	ActivityDiscount struct {
		Enable bool `json:"enable"`
		Data   struct {
			Current struct {
				DiscountType string  `json:"discount_type"`
				Before       float64 `json:"before_consumption_rate"`
				After        float64 `json:"consumption_rate"`
			} `json:"current"`
			Member struct {
				Before float64 `json:"before_consumption_rate"`
				After  float64 `json:"after_consumption_rate"`
			} `json:"member"`
			OffPeak struct {
				Before float64 `json:"before_consumption_rate"`
				After  float64 `json:"after_consumption_rate"`
			} `json:"off_peak"`
		} `json:"data"`
	} `json:"activity_discount"`
}

// discountTag 把活动折扣映射成徽章：专属补贴/闲时折扣/会员限时优惠。
// 与 Trae 官方选择器的展示规则对齐（营销活动随时轮换，以当时数据为准）。
func (c *contactConfig) discountTag() (string, string) {
	ad := c.ActivityDiscount
	if !ad.Enable {
		return "", ""
	}
	cur := ad.Data.Current
	switch cur.DiscountType {
	case "subsidy":
		if cur.After > 0 && cur.After < cur.Before {
			return "专属补贴", "#14b8a6"
		}
	case "off_peak":
		if cur.After > 0 && cur.After < cur.Before {
			return "闲时折扣", "#22c55e"
		}
	}
	if ad.Data.OffPeak.After > 0 && ad.Data.OffPeak.After < ad.Data.OffPeak.Before {
		return "闲时折扣", "#22c55e"
	}
	if ad.Data.Member.After > 0 && ad.Data.Member.After < ad.Data.Member.Before {
		return "会员限时优惠", "#22c55e"
	}
	return "", ""
}

// parseContactConfig 解析 display_contact_config JSON 字符串，失败返回零值。
func parseContactConfig(raw string) *contactConfig {
	if raw == "" {
		return nil
	}
	var cc contactConfig
	if err := json.Unmarshal([]byte(raw), &cc); err != nil {
		return nil
	}
	return &cc
}

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
			ConfigName        string `json:"config_name"`
			ContextWindow     struct {
				Dev int64 `json:"dev"`
			} `json:"context_window_tokens"`
			DisplayConfig struct {
				DisplayName   string   `json:"display_name"`
				Multimodal    bool     `json:"multimodal"`
				IsBeta        bool     `json:"is_beta"`
				Tags          []string `json:"tags"`
				ModelCapability string `json:"model_capability"`
			} `json:"display_config"`
			DisplayContactConfig string          `json:"display_contact_config"`
			ReasoningEffort      json.RawMessage `json:"reasoning_effort_config"`
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
		dc := c.DisplayConfig
		name := dc.DisplayName
		if name == "" {
			name = c.ConfigName
		}
		mi := provider.ModelInfo{
			ID:       c.ConfigName,
			Name:     name,
			MaxInput: c.ContextWindow.Dev,
			Vision:   dc.Multimodal,
			// SOLO 协议原生支持工具：OpenAI tools 双向转换（solo.go），
			// 实测 GLM/DeepSeek/Kimi 家族均能正确返回 tool_calls。
			Tools: true,
		}
		// 思考能力：优先 reasoning_effort_config.support_thinking，
		// 该字段缺失时回退 model_capability == "reasoning_model"。
		if len(c.ReasoningEffort) > 0 {
			var re struct {
				SupportThinking bool `json:"support_thinking"`
				DefaultLevel    string `json:"default_level"`
			}
			if err := json.Unmarshal(c.ReasoningEffort, &re); err == nil {
				mi.Reasoning = re.SupportThinking || re.DefaultLevel != ""
			}
		}
		if !mi.Reasoning && dc.ModelCapability == "reasoning_model" {
			mi.Reasoning = true
		}
		// 计费倍率与优惠徽章：display_contact_config（JSON 字符串）
		if cc := parseContactConfig(c.DisplayContactConfig); cc != nil {
			if cc.ConsumptionRate.Enable && cc.ConsumptionRate.Data.Rate > 0 {
				mi.Credits = "x" + strconv.FormatFloat(cc.ConsumptionRate.Data.Rate, 'f', -1, 64)
			}
			if text, color := cc.discountTag(); text != "" {
				mi.Tags = append([]string{text + "|" + color}, mi.Tags...)
			}
		}
		// 徽章：tags（如 Beta）加琥珀色
		for _, tag := range dc.Tags {
			if tag == "" {
				continue
			}
			color := ""
			if strings.EqualFold(tag, "beta") {
				color = "#f59e0b"
			}
			if color != "" {
				mi.Tags = append(mi.Tags, tag+"|"+color)
			} else {
				mi.Tags = append(mi.Tags, tag)
			}
		}
		if dc.IsBeta && !tagIn(mi.Tags, "Beta") {
			mi.Tags = append(mi.Tags, "Beta|#f59e0b")
		}
		list = append(list, mi)
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
