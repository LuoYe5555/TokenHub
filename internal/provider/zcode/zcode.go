package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
	"tokenhub/internal/winproc"
)

const appVersionFallback = "3.14.3"

var (
	appVerOnce   sync.Once
	appVerCached = appVersionFallback
)

// WarmAppVersion 启动时预热：读注册表拿本机 ZCode 客户端版本（billing 接口校验版本）。
func WarmAppVersion() {
	appVerOnce.Do(func() {
		if runtime.GOOS != "windows" {
			return
		}
		for _, hive := range []string{
			`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
			`HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
			`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		} {
			out, err := winproc.Cmd("reg", "query", hive, "/s", "/v", "DisplayVersion").CombinedOutput()
			if err != nil {
				continue
			}
			if v := extractZcodeVersion(string(out)); v != "" {
				appVerCached = v
				return
			}
		}
	})
}

func extractZcodeVersion(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "ZCode") {
			for j := i; j < len(lines) && j < i+8; j++ {
				f := strings.Fields(lines[j])
				for k, field := range f {
					if field == "DisplayVersion" && k+1 < len(f) {
						return f[k+1]
					}
				}
			}
		}
	}
	return ""
}

func appVersion() string {
	WarmAppVersion()
	return appVerCached
}

func platform() string {
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return runtime.GOOS + "-" + arch
}

func apiBase(pc *config.ProviderConfig) string {
	if pc != nil && pc.APIBase != "" {
		return strings.TrimRight(pc.APIBase, "/")
	}
	return APIBase
}

func bigmodelBase(pc *config.ProviderConfig) string {
	if pc != nil && pc.BillingBase != "" {
		return strings.TrimRight(pc.BillingBase, "/")
	}
	return BigmodelBase
}

// ── 请求头 ──

// planMessagesHeaders 对话网关头（镜像客户端真实流量，移植自 buildPlanRequest）。
func planMessagesHeaders(token string) map[string]string {
	ver := appVersion()
	return map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "*/*",
		"Accept-Encoding":      "identity",
		"User-Agent":           fmt.Sprintf("ZCode/%s ai-sdk/anthropic/3.0.81", ver),
		"X-ZCode-App-Version":  ver,
		"X-ZCode-Agent":        "glm",
		"X-Title":              "Z Code@cli",
		"HTTP-Referer":         "https://zcode.z.ai",
		"X-Client-Language":    "zh-CN",
		"X-Client-Timezone":    clientTimezone(),
		"X-Platform":           platform(),
		"X-Os-Category":        runtime.GOOS,
		"X-Release-Channel":    "production",
		"x-zcode-session-type": "main",
		"x-zcode-trace-id":     provider.NewUUID(),
		"x-request-id":         provider.NewUUID(),
		"anthropic-version":    "2023-06-01",
		"Authorization":        "Bearer " + token,
	}
}

// billingHeaders ZCode 计费/活动接口头（移植自 zaiHeaders）。
func billingHeaders(token, deviceMid string) map[string]string {
	ver := appVersion()
	h := map[string]string{
		"User-Agent":           fmt.Sprintf("ZCode/%s", ver),
		"HTTP-Referer":         "https://zcode.z.ai",
		"X-Title":              "Z Code@electron",
		"X-ZCode-App-Version":  ver,
		"X-Platform":           platform(),
		"X-Release-Channel":    "stable",
		"X-Client-Language":    "zh-CN",
		"X-Client-Timezone":    clientTimezone(),
		"X-Os-Category":        runtime.GOOS,
		"Authorization":        "Bearer " + token,
		"x-request-id":         provider.NewUUID(),
		"Content-Type":         "application/json",
		"Accept":               "application/json",
	}
	if deviceMid != "" {
		h["X-Device-Mid"] = deviceMid
	}
	return h
}

// bigmodelHeaders 智谱 BigModel 开放平台接口头。
func bigmodelHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"User-Agent":    fmt.Sprintf("ZCode/%s", appVersion()),
		"x-request-id":  provider.NewUUID(),
		"Accept":        "application/json",
	}
}

// CaptchaConfig 拉取验证码配置（GET /api/v1/client/configs，免认证接口）。
// 返回 data.configs.captcha 原始结构（含阿里云 Captcha 的 sceneId/prefix/region 等）。
// 注意（2026-10-06 实测）：platform 查询参数与 X-Platform 请求头（值为 windows-x64）
// 都会触发 3001 parameter error——该接口对 platform 校验不兼容，必须省略。
func (p *Provider) CaptchaConfig(ctx context.Context, acct *store.Account) (map[string]any, error) {
	pc := p.cfg.PC(store.PZCode)
	versions := []string{appVersion(), "3.0.1"}
	var captcha map[string]any
	for _, ver := range versions {
		h := billingHeaders("", deviceMid(acct))
		delete(h, "X-Platform") // 该接口带 X-Platform 会 3001
		url := apiBase(pc) + "/api/v1/client/configs?app_version=" + ver
		var out map[string]any
		if _, err := httpx.DoJSON(ctx, "GET", url, h, nil, &out, 20*time.Second); err != nil {
			return nil, err
		}
		data, _ := out["data"].(map[string]any)
		configs, _ := data["configs"].(map[string]any)
		captcha, _ = configs["captcha"].(map[string]any)
		if captcha == nil {
			// 兼容不同嵌套层级：逐层兜底查找
			if c2, ok := data["captcha"].(map[string]any); ok {
				captcha = c2
			}
		}
		if captcha != nil {
			return captcha, nil
		}
	}
	return nil, fmt.Errorf("client/configs 未返回验证码配置")
}

var (
	tzOnce   sync.Once
	tzCached string
)

func clientTimezone() string {
	tzOnce.Do(func() {
		tzCached = osTimezone()
	})
	if tzCached != "" {
		return tzCached
	}
	return "Asia/Shanghai"
}

func osTimezone() string {
	out, err := winproc.Cmd("powershell", "-NoProfile", "-Command", "(Get-TimeZone).Id").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// deviceMid 每账号虚拟设备 ID（没有则生成并落盘，由调用方保存账号）。
func deviceMid(acct *store.Account) string {
	if mid := acct.ExtraGet(store.ExtraZCodeDeviceMid); mid != "" {
		return mid
	}
	mid := provider.NewUUID()
	acct.ExtraSet(store.ExtraZCodeDeviceMid, mid)
	return mid
}

// ── token 候选 ──

// planToken 对话/领取用 token（zcodejwttoken 优先）。
func (p *Provider) planToken(acct *store.Account) (string, error) {
	if t := strings.TrimSpace(acct.AccessToken); t != "" {
		return t, nil
	}
	secret := DefaultSecret(localHome())
	if t := strings.TrimSpace(SafeDecrypt(rawCredential(acct, "zcodejwttoken"), secret)); t != "" {
		return t, nil
	}
	return "", provider.Err(provider.KindAuth, 0, "账号快照里没有可用的 zcodejwttoken，请重新登录/导入")
}

// quotaTokens 额度查询候选 token。
func (p *Provider) quotaTokens(acct *store.Account) []string {
	var out []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if len(t) > 20 {
			for _, e := range out {
				if e == t {
					return
				}
			}
			out = append(out, t)
		}
	}
	add(acct.AccessToken)
	secret := DefaultSecret(localHome())
	add(SafeDecrypt(rawCredential(acct, "zcodejwttoken"), secret))
	add(acct.ExtraGet(store.ExtraZCodeBigmodelAT))
	add(acct.ExtraGet(store.ExtraZCodeZaiAT))
	return out
}

func rawCredential(acct *store.Account, key string) string {
	raw := acct.ExtraGet(store.ExtraZCodeCredentials)
	if raw == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func localHome() string {
	if lp, ok := ResolveLocalPaths(); ok {
		return lp.Home
	}
	return ""
}

// ── Provider ──

type Provider struct {
	cfg            *config.Config
	pendMu         sync.Mutex
	pending        map[string][]pendingClaim // accountID → 待人工验证领取的活动
	captchaTrigger func()                    // 撞到验证码时的回调（自动滑块）
}

func New(cfg *config.Config) *Provider {
	// 连接预热：保持对话上游热连接，请求时免握手
	httpx.RegisterWarm(apiBase(cfg.PC(store.PZCode)))
	return &Provider{cfg: cfg}
}

// SyncFromLocal 从本机 ZCode 客户端重新同步凭据（按 UID 匹配）。
// ZCode 的 JWT 无刷新端点，过期只能重读客户端当前登录。
func (p *Provider) SyncFromLocal(acct *store.Account) error {
	creds, ok, err := ReadLocal()
	if err != nil || !ok {
		return provider.Err(provider.KindAuth, 0, "本机 ZCode 未登录或读取凭据失败: %v", err)
	}
	l := creds.ToAccount()
	if l == nil {
		return provider.Err(provider.KindAuth, 0, "本机 ZCode 未登录（凭据为空）")
	}
	if acct.UID != "" && l.UID != "" && l.UID != acct.UID {
		return provider.Err(provider.KindAuth, 0,
			"本机 ZCode 当前登录的是另一个账号（%s），请在 ZCode 里切换回该账号后重试", l.DisplayedName())
	}
	acct.AccessToken = l.AccessToken
	for k, v := range l.Extra {
		acct.ExtraSet(k, v)
	}
	acct.Dead = false
	acct.DeadReason = ""
	return nil
}

func (p *Provider) Name() string { return store.PZCode }

// Refresh ZCode JWT 无已验证的刷新端点；401 时标记重新登录。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error {
	return provider.Err(provider.KindAuth, 0, "ZCode 无刷新机制，请在客户端重新登录后重新导入")
}

// ── 对话 ──

func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	pc := p.cfg.PC(store.PZCode)
	token, err := p.planToken(acct)
	if err != nil {
		sink.Close()
		return err
	}
	body, err := BuildAnthropicFromOpenAI(openaiReq, pc.DefaultModel, 16384)
	if err != nil {
		sink.Close()
		return err
	}
	applyShape(body, p.cfg.ZCodeInjectShape, stableSessionHex(acct.ID))
	raw, err := json.Marshal(body)
	if err != nil {
		sink.Close()
		return err
	}
	h := planMessagesHeaders(token)
	if mid := acct.ExtraGet(store.ExtraZCodeDeviceMid); mid != "" {
		h["X-Device-Mid"] = mid
	}
	resp, err := httpx.DoStream(ctx, "POST", apiBase(pc)+"/api/v1/zcode-plan/anthropic/v1/messages", h, raw, p.cfg.ConnectTimeout())
	if err != nil {
		sink.Close()
		return provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	defer func() {
		if resp.Stream != nil {
			resp.Stream.Close()
		}
	}()
	if resp.Status >= 400 {
		sink.Close()
		return classifyZcode(resp.Status, resp.Body)
	}
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		// 200 + JSON = 业务错误信封（额度耗尽/验证码等）
		sink.Close()
		return classifyZcode(resp.Status, resp.Body)
	}
	return StreamAnthropic(resp.Stream, sink)
}

var captchaRe = regexp.MustCompile(`"code"\s*:\s*(3007|3012)\b|\b(3007|3012)\b|captcha|unusual activity`)
var zcodeAuthRe = regexp.MustCompile(`"code"\s*:\s*401\b|\b401\b|令牌已过期|验证不正确`)
var zcodeQuotaRe = regexp.MustCompile(`"code"\s*:\s*(1005|1113)\b|\b(1113|1005)\b|余额不足|无可用资源包|exceed quota|insufficient`)

func classifyZcode(status int, body []byte) *provider.Error {
	text := string(body)
	switch {
	case status == 405 || captchaRe.MatchString(text):
		return provider.Err(provider.KindCaptcha, status, "需要验证码（3007）")
	case status == 401 || zcodeAuthRe.MatchString(text):
		return provider.Err(provider.KindAuth, status, "JWT 已失效，请重新登录导入")
	case status == 402 || zcodeQuotaRe.MatchString(text) || strings.Contains(text, "余额不足") || strings.Contains(text, "无可用资源包") || strings.Contains(strings.ToLower(text), "insufficient"):
		return provider.Err(provider.KindQuota, status, "%s", cutText(text, 200))
	}
	return provider.ClassifyBody(status, body)
}

func cutText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "(empty)"
	}
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// ── 额度 ──

func businessOk(m map[string]any) bool {
	code, has := m["code"]
	if !has {
		return m["success"] != false
	}
	switch v := code.(type) {
	case float64:
		return v == 0 || v == 200
	case string:
		return v == "0" || v == "200"
	}
	return true
}

func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	pc := p.cfg.PC(store.PZCode)
	tokens := p.quotaTokens(acct)
	if len(tokens) == 0 {
		return nil, provider.Err(provider.KindAuth, 0, "账号没有可用 token，请重新登录/导入")
	}
	var lastErr error
	for _, t := range tokens {
		// 1) BigModel Coding Plan 窗口额度
		var limit map[string]any
		st, err := httpx.DoJSON(ctx, "GET", bigmodelBase(pc)+"/api/monitor/usage/quota/limit", bigmodelHeaders(t), nil, &limit, 20*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		_ = st
		if businessOk(limit) {
			var sub map[string]any
			_, _ = httpx.DoJSON(ctx, "GET", bigmodelBase(pc)+"/api/biz/subscription/list", bigmodelHeaders(t), nil, &sub, 20*time.Second)
			snap := normalizeQuotaLimit(limit, sub)
			if snap != nil && len(snap.Parts) > 0 {
				snap.UpdatedAt = time.Now().Unix()
				snap.Source = "bigmodel"
				return snap, nil
			}
		}
		if codeOf(limit) == 401 {
			lastErr = provider.Err(provider.KindAuth, 401, "鉴权失败")
		} else if msg, _ := limit["msg"].(string); msg != "" {
			lastErr = fmt.Errorf("%s", msg)
		}
	}
	// 2) Z.ai / Start Plan 余额：balance + current + 套餐权益 三层合并（体验包/活动包在权益里）
	jwt, jerr := p.planToken(acct)
	if jerr == nil {
		h := billingHeaders(jwt, deviceMid(acct))
		var bal, cur map[string]any
		balOK, curOK := false, false
		if _, err := httpx.DoJSON(ctx, "GET", apiBase(pc)+fmt.Sprintf("/api/v1/zcode-plan/billing/balance?app_version=%s", appVersion()), h, nil, &bal, 25*time.Second); err == nil && businessOk(bal) {
			balOK = true
		} else if err != nil {
			lastErr = err
		} else if codeOf(bal) == 401 {
			lastErr = provider.Err(provider.KindAuth, 401, "鉴权失败")
		}
		if _, err := httpx.DoJSON(ctx, "GET", apiBase(pc)+fmt.Sprintf("/api/v1/zcode-plan/billing/current?app_version=%s", appVersion()), h, nil, &cur, 25*time.Second); err == nil && businessOk(cur) {
			curOK = true
		}
		if balOK || curOK {
			snap := mergeZcodeQuota(bal, cur)
			snap.UpdatedAt = time.Now().Unix()
			snap.Source = "zcode.z.ai"
			return snap, nil
		}
	}
	if lastErr == nil {
		return &store.QuotaSnapshot{UpdatedAt: time.Now().Unix(), Note: "无有效套餐（免费额度按日发放）", Source: "none"}, nil
	}
	if e, ok := lastErr.(*provider.Error); ok {
		return nil, e
	}
	return nil, fmt.Errorf("额度查询失败: %w", lastErr)
}

func codeOf(m map[string]any) int {
	if m == nil {
		return 0
	}
	switch v := m["code"].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// normalizeQuotaLimit BigModel quota/limit → 快照（移植自 zcodeClient.normalizeQuotaLimit）。
func normalizeQuotaLimit(limitResp, subResp map[string]any) *store.QuotaSnapshot {
	snap := &store.QuotaSnapshot{Unit: ""}
	data, _ := limitResp["data"].(map[string]any)
	limits, _ := data["limits"].([]any)
	for _, lv := range limits {
		l, ok := lv.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := l["type"].(string)
		part := store.QuotaPart{}
		switch typ {
		case "TOKENS_LIMIT":
			part.Name = "提示次数"
			part.Unit = "次"
		case "TIME_LIMIT":
			part.Name = "使用时长"
			part.Unit = "分钟"
		default:
			part.Name = typ
		}
		part.Total = toF(l["usage"])
		part.Used = toF(l["currentValue"])
		if r, ok := l["remaining"].(float64); ok {
			part.Remain = r
		} else {
			part.Remain = part.Total - part.Used
			if part.Remain < 0 {
				part.Remain = 0
			}
		}
		snap.Parts = append(snap.Parts, part)
	}
	if subData, ok := subResp["data"].([]any); ok && len(subData) > 0 {
		for _, sv := range subData {
			s, ok := sv.(map[string]any)
			if !ok {
				continue
			}
			if s["status"] == "VALID" {
				if name, _ := s["productName"].(string); name != "" {
					snap.Plan = name
				}
				break
			}
		}
	}
	// 主额度优先“使用时长”
	var main *store.QuotaPart
	for i := range snap.Parts {
		if snap.Parts[i].Unit == "分钟" && snap.Parts[i].Total > 0 {
			main = &snap.Parts[i]
			break
		}
	}
	if main == nil {
		for i := range snap.Parts {
			if snap.Parts[i].Total > 0 {
				main = &snap.Parts[i]
				break
			}
		}
	}
	if main != nil {
		snap.Total, snap.Used, snap.Remain, snap.Unit = main.Total, main.Used, main.Remain, main.Unit
	}
	return snap
}

// normalizeBalance billing/balance → 快照（移植自 zcodeClient.normalizeBalance）。
func normalizeBalance(bal map[string]any) *store.QuotaSnapshot {
	return mergeZcodeQuota(bal, nil)
}

// mergeZcodeQuota 三层合并额度：
//  1. billing/balance 的 balances[]（权威剩余量，优先）
//  2. 生效套餐 plans[].entitlements 里的 token 权益（体验包/活动包常只出现在这里；
//     与 balances 同名的跳过防重复计数，待生效的标注不计入合计）
//  3. billing/current 的套餐总量（前两层都为空时兜底）
func mergeZcodeQuota(bal, cur map[string]any) *store.QuotaSnapshot {
	snap := &store.QuotaSnapshot{Unit: "Token"}
	seen := map[string]bool{}
	addPart := func(pt store.QuotaPart) {
		k := strings.ToLower(strings.TrimSpace(pt.Name))
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		snap.Parts = append(snap.Parts, pt)
	}

	var balData, curData map[string]any
	if bal != nil {
		balData, _ = bal["data"].(map[string]any)
	}
	if cur != nil {
		curData, _ = cur["data"].(map[string]any)
	}
	now := time.Now().Unix()
	pendingCount := 0
	planName := ""

	// 套餐名与待生效统计（balance 与 current 的 plans 都看）
	collectPlans := func(data map[string]any) []map[string]any {
		if data == nil {
			return nil
		}
		plans, _ := data["plans"].([]any)
		var out []map[string]any
		for _, pv := range plans {
			if p, ok := pv.(map[string]any); ok {
				out = append(out, p)
				if strings.EqualFold(strOr(p["status"]), "active") && planName == "" {
					planName = strOr(p["name"], p["plan_id"])
				}
			}
		}
		return out
	}

	// 1) balances[]
	if balData != nil {
		balances, _ := balData["balances"].([]any)
		for _, bv := range balances {
			b, ok := bv.(map[string]any)
			if !ok {
				continue
			}
			total := toF(b["total_units"])
			used := toF(b["used_units"])
			remain := toF(b["remaining_units"])
			if remain == 0 {
				remain = toF(b["available_units"])
			}
			if remain == 0 && total > 0 {
				remain = total - used
			}
			unit := strOr(b["unit_type"], "Token")
			if unit == "token" {
				unit = "Token"
			}
			name := strOr(b["show_name"], b["name"], b["entitlement_id"], b["plan_id"], "额度")
			addPart(store.QuotaPart{Name: name, Unit: unit, Total: total, Used: used, Remain: remain})
		}
	}

	// 2) 生效套餐权益（体验包/活动包）
	for _, p := range collectPlans(balData) {
		ents, _ := p["entitlements"].([]any)
		for _, ev := range ents {
			e, ok := ev.(map[string]any)
			if !ok || strOr(e["unit_type"]) != "token" {
				continue
			}
			grant := toF(e["grant_units"])
			if grant == 0 {
				grant = toF(e["grantUnits"])
			}
			if grant <= 0 {
				continue
			}
			name := strOr(e["show_name"], e["showName"], e["entitlement_id"], e["entitlementId"], "活动额度")
			if seen[strings.ToLower(strings.TrimSpace(name))] {
				continue // balances 已计入
			}
			eff := epochSec(e["effective_at"])
			if eff == 0 {
				eff = epochSec(e["effectiveAt"])
			}
			if eff > now {
				pendingCount++ // 待生效：不计入合计
				continue
			}
			addPart(store.QuotaPart{Name: name, Unit: "Token", Total: grant, Remain: grant})
		}
	}

	// 3) billing/current：只补 balance 里没有的套餐（按 plan_id 去重，避免同一池子双算），
	//    独立领取的体验包通常在这里以单独 plan 出现
	if curData != nil {
		balPlanIDs := map[string]bool{}
		for _, p := range collectPlans(balData) {
			if id := strOr(p["plan_id"]); id != "" {
				balPlanIDs[id] = true
			}
		}
		for _, p := range collectPlans(curData) {
			id := strOr(p["plan_id"])
			if id != "" && balPlanIDs[id] {
				continue
			}
			total := toF(p["total_units"])
			if total <= 0 {
				continue
			}
			used := toF(p["used_units"])
			remain := toF(p["available_units"])
			if remain == 0 {
				remain = toF(p["availableUnits"])
			}
			if remain == 0 {
				remain = total - used
			}
			name := strOr(p["name"], id, "套餐额度")
			addPart(store.QuotaPart{Name: name, Unit: "Token", Total: total, Used: used, Remain: remain})
		}
	}

	for _, pt := range snap.Parts {
		snap.Total += pt.Total
		snap.Used += pt.Used
		snap.Remain += pt.Remain
	}
	snap.Plan = planName
	if pendingCount > 0 {
		snap.Note = fmt.Sprintf("另有 %d 项待生效权益", pendingCount)
	} else if len(snap.Parts) == 0 && planName == "" {
		snap.Note = "无有效套餐（免费额度按日发放）"
	}
	return snap
}

func strOr(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toF(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func epochSec(v any) int64 {
	f := toF(v)
	if f <= 0 {
		return 0
	}
	if f < 1e12 {
		return int64(f)
	}
	return int64(f / 1000)
}

// ── 活动领取（无每日签到，用「领取全部可领套餐」实现自动领 tokens） ──

// ClaimPlanInfo preview 返回的可领取活动。
type ClaimPlanInfo struct {
	PlanID      string   `json:"planId"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Priority    float64  `json:"priority"`
	Grants      []string `json:"grants,omitempty"`
}

// pendingClaim 自动领取时撞到验证码的活动待办（等人工滑块后领取）。
type pendingClaim struct {
	PlanID string `json:"planId"`
	Name   string `json:"name"`
}

// SetCaptchaTrigger 注册"撞到验证码"时的回调（面板用来自动弹窗 + SendInput 自动滑块）。
func (p *Provider) SetCaptchaTrigger(fn func()) {
	p.captchaTrigger = fn
}

func (p *Provider) addPendingClaim(acctID, planID, name string) {
	p.pendMu.Lock()
	defer p.pendMu.Unlock()
	if p.pending == nil {
		p.pending = map[string][]pendingClaim{}
	}
	exists := false
	for _, pc := range p.pending[acctID] {
		if pc.PlanID == planID {
			exists = true
			break
		}
	}
	if !exists {
		p.pending[acctID] = append(p.pending[acctID], pendingClaim{planID, name})
	}
	if fn := p.captchaTrigger; fn != nil {
		go fn() // 异步触发，不阻塞领取循环
	}
}

func (p *Provider) ClearPendingClaim(acctID, planID string) {
	p.pendMu.Lock()
	defer p.pendMu.Unlock()
	list := p.pending[acctID]
	out := list[:0]
	for _, pc := range list {
		if pc.PlanID != planID {
			out = append(out, pc)
		}
	}
	p.pending[acctID] = out
}

// PendingClaims 某账号待人工验证领取的活动（面板横幅展示）。
func (p *Provider) PendingClaims(acctID string) []ClaimPlanInfo {
	p.pendMu.Lock()
	defer p.pendMu.Unlock()
	var out []ClaimPlanInfo
	for _, pc := range p.pending[acctID] {
		out = append(out, ClaimPlanInfo{PlanID: pc.PlanID, Name: pc.Name})
	}
	return out
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return p.cfg.ZCodeAutoClaim }

// Checkin 自动领取：preview → 逐个 claim（不带验证码；需验证码的活动会在消息中说明）。
func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	plans, err := p.ClaimPlans(ctx, acct)
	if err != nil {
		return false, "", err
	}
	if len(plans) == 0 {
		return true, "暂无可领取活动", nil
	}
	var msgs []string
	var lastErr error
	claimed := 0
	for _, plan := range plans {
		res, already, err := p.ClaimPlan(ctx, acct, plan.PlanID, "", "")
		if err != nil {
			lastErr = err
			msgs = append(msgs, fmt.Sprintf("%s: %v", plan.Name, err))
			// 撞到验证码 → 记入待办，面板横幅提示人工滑块领取
			if strings.Contains(err.Error(), "验证码") || strings.Contains(err.Error(), "captcha") {
				p.addPendingClaim(acct.ID, plan.PlanID, plan.Name)
			}
			continue
		}
		p.ClearPendingClaim(acct.ID, plan.PlanID)
		if already {
			msgs = append(msgs, fmt.Sprintf("%s 已领取过", res))
			continue
		}
		claimed++
		msgs = append(msgs, fmt.Sprintf("已领取 %s", res))
	}
	summary := strings.Join(msgs, "；")
	if claimed == 0 && lastErr != nil {
		return false, summary, lastErr
	}
	return false, summary, nil
}

// ClaimPlans 查询可领取活动列表。
func (p *Provider) ClaimPlans(ctx context.Context, acct *store.Account) ([]ClaimPlanInfo, error) {
	pc := p.cfg.PC(store.PZCode)
	token, err := p.planToken(acct)
	if err != nil {
		return nil, err
	}
	h := billingHeaders(token, deviceMid(acct))
	url := apiBase(pc) + fmt.Sprintf("/api/v1/zcode-plan/billing/preview?app_version=%s&platform=%s", appVersion(), platform())
	var out map[string]any
	if _, err := httpx.DoJSON(ctx, "GET", url, h, nil, &out, 25*time.Second); err != nil {
		return nil, err
	}
	if codeOf(out) != 0 {
		msg, _ := out["msg"].(string)
		if msg == "" {
			msg, _ = out["message"].(string)
		}
		return nil, fmt.Errorf("查询活动列表失败: %s", msg)
	}
	data, _ := out["data"].(map[string]any)
	plans, _ := data["plans"].([]any)
	var list []ClaimPlanInfo
	for _, pv := range plans {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		id, _ := p["plan_id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		info := ClaimPlanInfo{
			PlanID:      id,
			Name:        strOr(p["name"], id),
			Description: strOr(p["description"]),
			Priority:    toF(p["priority"]),
		}
		if ents, ok := p["entitlements"].([]any); ok {
			for _, ev := range ents {
				e, ok := ev.(map[string]any)
				if !ok {
					continue
				}
				if strOr(e["meter"]) != "model_usage" || strOr(e["unit_type"]) != "token" {
					continue
				}
				name := strOr(e["show_name"])
				if name == "" {
					continue
				}
				info.Grants = append(info.Grants, fmt.Sprintf("%s %.0f Token (%s)", name, toF(e["grant_units"]), strOr(e["period"], "one_time")))
			}
		}
		list = append(list, info)
	}
	// priority 高的排前
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Priority > list[j-1].Priority; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	return list, nil
}

// ClaimResult 领取结果。
type ClaimResult struct {
	PlanName string `json:"planName"`
	Detail   string `json:"detail,omitempty"`
}

var claimFailMsg = map[int64]string{
	1001: "套餐不存在",
	1002: "活动已结束或暂不可领取",
	1003: "该套餐已领取过",
	1004: "不符合领取条件",
	1005: "今日领取名额已用完",
	3001: "领取参数错误，请刷新后重试",
	3007: "需要验证码（面板手动领取）",
	401:  "请先登录后再领取",
}

// ClaimPlan 领取指定活动套餐。already=true 表示上游返回「已领取过」——tokens 实际已到账，
// 按幂等成功返回（并发场景：定时任务与手动领取可能同时进行，后到的请求拿到 1003，
// 若当失败处理会把验证码 SDK 的滑块重置成手动模式）。
func (p *Provider) ClaimPlan(ctx context.Context, acct *store.Account, planID, captchaParam, region string) (name string, already bool, err error) {
	pc := p.cfg.PC(store.PZCode)
	token, err := p.planToken(acct)
	if err != nil {
		return "", false, err
	}
	h := billingHeaders(token, deviceMid(acct))
	if captchaParam != "" {
		h["X-Aliyun-Captcha-Verify-Param"] = captchaParam
	}
	if region != "" {
		h["X-Aliyun-Captcha-Verify-Region"] = region
	}
	body := map[string]any{"plan_id": planID}
	var out map[string]any
	if _, err := httpx.DoJSON(ctx, "POST", apiBase(pc)+"/api/v1/zcode-plan/billing/claim", h, body, &out, 30*time.Second); err != nil {
		return "", false, err
	}
	if codeOf(out) != 0 {
		code := int64(codeOf(out))
		if code == 1003 { // 已领取过：幂等成功
			return planID, true, nil
		}
		serverMsg := strOr(out["msg"], out["message"])
		base := claimFailMsg[code]
		if base == "" {
			base = "领取失败"
		}
		if serverMsg != "" {
			return "", false, fmt.Errorf("%s（%s）", base, serverMsg)
		}
		return "", false, fmt.Errorf("%s", base)
	}
	planName := planID
	if d, ok := out["data"].(map[string]any); ok {
		if pl, ok := d["plan"].(map[string]any); ok {
			if n := strOr(pl["name"]); n != "" {
				planName = n
			}
		}
	}
	logx.Infof("zcode", "账号 %s 领取成功: %s", acct.DisplayedName(), planName)
	return planName, false, nil
}

// RawQuotaSources 返回额度相关上游接口的原始响应（面板「原始数据」诊断用）。
func (p *Provider) RawQuotaSources(ctx context.Context, acct *store.Account) map[string]any {
	pc := p.cfg.PC(store.PZCode)
	out := map[string]any{}
	get := func(key, url string, h map[string]string) {
		var v any
		if _, err := httpx.DoJSON(ctx, "GET", url, h, nil, &v, 25*time.Second); err != nil {
			out[key] = map[string]any{"error": err.Error()}
		} else {
			out[key] = v
		}
	}
	for i, t := range p.quotaTokens(acct) {
		h := bigmodelHeaders(t)
		get(fmt.Sprintf("bigmodel_quota_limit[%d]", i), bigmodelBase(pc)+"/api/monitor/usage/quota/limit", h)
		get(fmt.Sprintf("bigmodel_subscription[%d]", i), bigmodelBase(pc)+"/api/biz/subscription/list", h)
	}
	if jwt, err := p.planToken(acct); err == nil {
		h := billingHeaders(jwt, deviceMid(acct))
		get("billing_balance", apiBase(pc)+fmt.Sprintf("/api/v1/zcode-plan/billing/balance?app_version=%s", appVersion()), h)
		get("billing_current", apiBase(pc)+fmt.Sprintf("/api/v1/zcode-plan/billing/current?app_version=%s", appVersion()), h)
		get("billing_preview", apiBase(pc)+fmt.Sprintf("/api/v1/zcode-plan/billing/preview?app_version=%s&platform=%s", appVersion(), platform()), h)
	} else {
		out["plan_token_error"] = err.Error()
	}
	return out
}

// ── 模型 ──

var seedModels = []provider.ModelInfo{
	{ID: "glm-5.3"}, {ID: "glm-5.3-flash"}, {ID: "glm-5.2"}, {ID: "glm-5-turbo"}, {ID: "glm-5"},
	{ID: "glm-4.7"}, {ID: "glm-4.7-flash"}, {ID: "glm-4.6"},
	{ID: "deepseek-v4-pro"}, {ID: "deepseek-v4-flash"},
	{ID: "kimi-k2.6"}, {ID: "kimi-k2.5"},
	{ID: "qwen3.5-plus"}, {ID: "qwen3-max"},
	{ID: "minimax-m3"},
}

func (p *Provider) SeedModels() []provider.ModelInfo { return seedModels }

func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	// Plan 渠道无公开模型目录端点，直接用静态表
	return seedModels, nil
}
