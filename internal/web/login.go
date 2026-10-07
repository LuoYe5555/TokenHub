package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tokenhub/internal/localimport"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider/trae"
	"tokenhub/internal/provider/workbuddy"
	"tokenhub/internal/provider/zcode"
	"tokenhub/internal/store"
)

// ── WorkBuddy 设备码登录 ──

func (p *Panel) handleWBLoginStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Realm string `json:"realm"`
	}
	_ = readBody(r, &in)
	if in.Realm != "cn" && in.Realm != "global" {
		in.Realm = "cn"
	}
	prov, ok := p.providerFor(store.PWorkBuddy).(*workbuddy.Provider)
	if !ok {
		writeErr(w, 500, "workbuddy 提供商未注册")
		return
	}
	state, authURL, err := prov.LoginStart(in.Realm)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	p.mu.Lock()
	p.wbStates[state] = in.Realm
	p.mu.Unlock()
	writeJSON(w, map[string]any{"state": state, "authUrl": authURL})
}

func (p *Panel) handleWBLoginPoll(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	p.mu.Lock()
	realm, ok := p.wbStates[state]
	p.mu.Unlock()
	if !ok {
		writeErr(w, 404, "登录会话不存在或已过期")
		return
	}
	prov, ok := p.providerFor(store.PWorkBuddy).(*workbuddy.Provider)
	if !ok {
		writeErr(w, 500, "workbuddy 提供商未注册")
		return
	}
	done, acct, err := prov.LoginPoll(realm, state)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if !done {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}
	p.mu.Lock()
	delete(p.wbStates, state)
	p.mu.Unlock()
	if err := p.store.Upsert(acct); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logx.Infof("panel", "WorkBuddy(%s) 账号已添加: %s", realm, acct.DisplayedName())
	// 添加后立刻查询额度 + 尝试签到
	go p.afterAdd(acct)
	writeJSON(w, map[string]any{"status": "ok", "nickname": acct.DisplayedName(), "id": acct.ID})
}

// afterAdd 添加账号后异步刷一次额度并尝试签到。
func (p *Panel) afterAdd(acct *store.Account) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prov := p.providerFor(acct.Provider)
	if prov == nil {
		return
	}
	if snap, err := prov.Quota(ctx, acct); err == nil {
		_ = p.store.Mutate(acct.ID, func(a *store.Account) { a.LastQuota = snap })
	}
	if prov.SupportsCheckin(acct) {
		if _, msg, err := prov.Checkin(ctx, acct); err == nil {
			today := time.Now().Format("2006-01-02")
			_ = p.store.Mutate(acct.ID, func(a *store.Account) {
				a.LastCheckinMsg = msg
				a.LastCheckinAt = time.Now().Unix()
				if acct.Provider != store.PZCode {
					a.LastCheckinDay = today
				}
			})
			logx.Infof(string(acct.Provider), "账号 %s: %s", acct.DisplayedName(), msg)
		}
	}
}

// ── Trae 授权登录（回调服务器 + 手动粘贴双通道） ──

const traeCallbackPort = "18080"

func (p *Panel) handleTraeLoginStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.providerFor(store.PTrae).(*trae.Provider); !ok {
		writeErr(w, 500, "trae 提供商未注册")
		return
	}
	callbackURL := "http://127.0.0.1:" + traeCallbackPort + "/authorize"
	p.ensureTraeCallback()

	machineID := randHex32()
	deviceID := randHex32()
	consoleBase := p.cfg.PC(store.PTrae).ConsoleBase
	loginURL := trae.BuildLoginURL(machineID, deviceID, callbackURL, consoleBase)

	pendingID := strings.ToLower(store.NewID())
	p.mu.Lock()
	p.trae[pendingID] = &traePending{
		machineID: machineID,
		deviceID:  deviceID,
		state:     "pending",
		createdAt: time.Now(),
	}
	p.mu.Unlock()
	writeJSON(w, map[string]any{"pendingId": pendingID, "loginUrl": loginURL, "callbackUrl": callbackURL})
}

func (p *Panel) handleTraeLoginPoll(w http.ResponseWriter, r *http.Request) {
	pendingID := r.URL.Query().Get("pendingId")
	p.mu.Lock()
	pending, ok := p.trae[pendingID]
	if ok && time.Since(pending.createdAt) > 10*time.Minute {
		delete(p.trae, pendingID)
		ok = false
	}
	var st, uid, nick, errMsg, accountID string
	if ok {
		st, uid, nick, errMsg, accountID = pending.state, pending.uid, pending.nickname, pending.errMsg, pending.accountID
	}
	p.mu.Unlock()
	if !ok {
		writeErr(w, 404, "登录会话不存在或已过期")
		return
	}
	resp := map[string]any{"status": st}
	if st == "success" {
		resp["uid"] = uid
		resp["nickname"] = nick
		resp["id"] = accountID
	}
	if st == "failed" {
		resp["error"] = errMsg
	}
	writeJSON(w, resp)
}

// handleTraeLoginCallback 手动粘贴回调链接完成登录。
func (p *Panel) handleTraeLoginCallback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	acct, machineID, err := p.completeTraeCallback(context.Background(), in.URL, "", "")
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if machineID != "" {
		// 找到对应 pending 则标记成功
		p.mu.Lock()
		for _, pend := range p.trae {
			if pend.machineID == machineID {
				pend.state = "success"
				pend.accountID = acct.ID
				pend.uid = acct.UID
				pend.nickname = acct.DisplayedName()
			}
		}
		p.mu.Unlock()
	}
	writeJSON(w, map[string]any{"status": "ok", "nickname": acct.DisplayedName(), "id": acct.ID})
}

// ensureTraeCallback 启动 127.0.0.1:18080 回调接收服务（幂等）。
func (p *Panel) ensureTraeCallback() {
	p.mu.Lock()
	started := p.traeCBStarted
	p.traeCBStarted = true
	p.mu.Unlock()
	if started {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		acct, machineID, err := p.completeTraeCallback(r.Context(), r.URL.String(), "", "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			fmt.Fprintf(w, "<!doctype html><meta charset='utf-8'><body style='background:#12141a;color:#eee;font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh'><div><h2>登录失败</h2><p>%s</p></div></body>", err.Error())
			return
		}
		p.mu.Lock()
		for _, pend := range p.trae {
			if pend.machineID == machineID {
				pend.state = "success"
				pend.accountID = acct.ID
				pend.uid = acct.UID
				pend.nickname = acct.DisplayedName()
			}
		}
		p.mu.Unlock()
		fmt.Fprintf(w, "<!doctype html><meta charset='utf-8'><title>TokenHub</title><body style='background:#12141a;color:#eee;font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh'><div style='text-align:center'><div style='font-size:44px'>&#10004;</div><h2>登录成功：%s</h2><p>可以关闭此页面，回到 TokenHub 面板查看。</p></div></body>", acct.DisplayedName())
	})
	srv := &http.Server{Addr: "127.0.0.1:" + traeCallbackPort, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logx.Infof("trae", "登录回调服务监听 http://127.0.0.1:%s/authorize", traeCallbackPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logx.Warnf("trae", "回调服务启动失败（可用「粘贴回调链接」方式登录）: %v", err)
		}
	}()
}

// completeTraeCallback 解析回调 URL → 换 token → 存账号。返回账号与 machineID（用于匹配 pending）。
func (p *Panel) completeTraeCallback(ctx context.Context, rawURL, machineID, deviceID string) (*store.Account, string, error) {
	info, err := trae.ParseCallback(rawURL)
	if err != nil {
		return nil, "", err
	}
	// 从 URL 补充 machine/device id（pending 优先）
	q := parseQuery(rawURL)
	if machineID == "" {
		machineID = q.Get("machine_id")
	}
	if deviceID == "" {
		deviceID = q.Get("device_id")
	}
	p.mu.Lock()
	for _, pend := range p.trae {
		if pend.machineID == machineID && pend.deviceID != "" {
			deviceID = pend.deviceID
		}
	}
	p.mu.Unlock()
	prov, ok := p.providerFor(store.PTrae).(*trae.Provider)
	if !ok {
		return nil, "", fmt.Errorf("trae 提供商未注册")
	}
	var refreshToken string
	if info.RefreshToken != "" {
		refreshToken = info.RefreshToken
	} else {
		return nil, "", fmt.Errorf("回调缺少 refreshToken")
	}
	acct, err := prov.CompleteLogin(ctx, refreshToken, "", machineID, deviceID)
	if err != nil {
		p.mu.Lock()
		for _, pend := range p.trae {
			if pend.machineID == machineID {
				pend.state = "failed"
				pend.errMsg = err.Error()
			}
		}
		p.mu.Unlock()
		return nil, machineID, err
	}
	if info.UID != "" {
		acct.UID = info.UID
	}
	if info.Nickname != "" {
		acct.Nickname = info.Nickname
	}
	if info.EnterpriseID != "" {
		acct.EnterpriseID = info.EnterpriseID
	}
	if err := p.store.Upsert(acct); err != nil {
		return nil, machineID, err
	}
	logx.Infof("panel", "Trae 账号已添加: %s", acct.DisplayedName())
	go p.afterAdd(acct)
	return acct, machineID, nil
}

func parseQuery(rawURL string) url.Values {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Query()
	}
	return url.Values{}
}

// ── ZCode OAuth 登录 / 本机导入 / 活动领取 ──

func (p *Panel) handleZcodeLoginStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
	}
	_ = readBody(r, &in)
	sess, err := zcode.LoginStart(in.Provider)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	p.mu.Lock()
	p.zcodeSess[sess.FlowID] = sess
	p.mu.Unlock()
	writeJSON(w, map[string]any{"flowId": sess.FlowID, "authorizeUrl": sess.AuthorizeURL, "provider": sess.Provider})
}

func (p *Panel) handleZcodeLoginPoll(w http.ResponseWriter, r *http.Request) {
	flowID := r.URL.Query().Get("flowId")
	p.mu.Lock()
	sess, ok := p.zcodeSess[flowID]
	p.mu.Unlock()
	if !ok {
		writeErr(w, 404, "登录会话不存在或已过期")
		return
	}
	res, err := zcode.LoginPoll(r.Context(), sess)
	if err != nil {
		p.mu.Lock()
		delete(p.zcodeSess, flowID)
		p.mu.Unlock()
		writeErr(w, 502, err.Error())
		return
	}
	if res.Status != "ok" {
		writeJSON(w, map[string]any{"status": res.Status})
		return
	}
	p.mu.Lock()
	delete(p.zcodeSess, flowID)
	p.mu.Unlock()
	acct := res.Account
	// 同一 uid 去重：更新而不是新增
	if acct.UID != "" {
		for _, e := range p.store.ByProvider(store.PZCode) {
			if e.UID == acct.UID {
				acct.ID = e.ID
				break
			}
		}
	}
	if err := p.store.Upsert(acct); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logx.Infof("panel", "ZCode(%s) 账号已添加: %s", acct.Realm, acct.DisplayedName())
	go p.afterAdd(acct)
	writeJSON(w, map[string]any{"status": "ok", "nickname": acct.DisplayedName(), "id": acct.ID})
}

func (p *Panel) handleZcodeImportLocal(w http.ResponseWriter, r *http.Request) {
	creds, ok, err := zcode.ReadLocal()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !ok {
		writeErr(w, 400, "未检测到本机 ZCode 登录（~/.zcode/v2/credentials.json）")
		return
	}
	rawCreds, _ := json.Marshal(creds.Raw)
	acct := &store.Account{
		Provider:    store.PZCode,
		Realm:       creds.LoginProvider,
		UID:         creds.UID,
		Nickname:    creds.DisplayName,
		Email:       creds.Email,
		AccessToken: creds.JWT,
		Enabled:     true,
		CreatedAt:   time.Now().Unix(),
	}
	if acct.Nickname == "" {
		acct.Nickname = creds.Username
	}
	acct.ExtraSet(store.ExtraZCodeCredentials, string(rawCreds))
	if creds.BigmodelAT != "" {
		acct.ExtraSet(store.ExtraZCodeBigmodelAT, creds.BigmodelAT)
	}
	if creds.BigmodelRT != "" {
		acct.ExtraSet(store.ExtraZCodeBigmodelRT, creds.BigmodelRT)
	}
	if creds.ZaiAT != "" {
		acct.ExtraSet(store.ExtraZCodeZaiAT, creds.ZaiAT)
	}
	// 同一 uid 去重
	if acct.UID != "" {
		for _, e := range p.store.ByProvider(store.PZCode) {
			if e.UID == acct.UID {
				acct.ID = e.ID
				break
			}
		}
	}
	if acct.ID == "" {
		acct.ID = store.NewID()
	}
	if err := p.store.Upsert(acct); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logx.Infof("panel", "ZCode 本机导入成功: %s", acct.DisplayedName())
	go p.afterAdd(acct)
	writeJSON(w, map[string]any{"ok": true, "nickname": acct.DisplayedName(), "id": acct.ID})
}

// handleImportLocalWB 扫描本机 WorkBuddy / CodeBuddy 会话文件导入。
func (p *Panel) handleImportLocalWB(w http.ResponseWriter, r *http.Request) {
	accts := localimport.WorkBuddyLocal()
	if len(accts) == 0 {
		writeErr(w, 400, "未在本机找到 WorkBuddy / CodeBuddy 登录（%LOCALAPPDATA%\\CodeBuddyExtension\\Data\\Public\\auth）")
		return
	}
	added := localimport.AddAllUnique(p.store, store.PWorkBuddy, accts)
	for _, a := range accts {
		if a.ID != "" {
			go p.afterAdd(a)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "found": len(accts), "added": added})
}

// handleImportLocalTrae 读取本机 Trae 客户端登录（tc 信封解密，不导入 refreshToken 以免踢掉 IDE 登录）。
func (p *Panel) handleImportLocalTrae(w http.ResponseWriter, r *http.Request) {
	accts := localimport.TraeLocal()
	if len(accts) == 0 {
		dir := "未找到"
		if d := localimport.TraeDirForLog(); d != "" {
			dir = d
		}
		writeErr(w, 400, "本机 Trae 数据目录："+dir+"；已找到 storage.json 但解密失败时，多为 Trae 版本更新了加密表，请反馈日志（data\\logs）")
		return
	}
	added := localimport.AddAllUnique(p.store, store.PTrae, accts)
	for _, a := range accts {
		if a.ID != "" {
			go p.afterAdd(a)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "found": len(accts), "added": added})
}

func (p *Panel) handleZcodePlans(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	acct := p.store.Get(id)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	prov, ok := p.providerFor(store.PZCode).(*zcode.Provider)
	if !ok {
		writeErr(w, 500, "zcode 提供商未注册")
		return
	}
	plans, err := prov.ClaimPlans(r.Context(), acct)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"plans": plans})
}

func (p *Panel) handleZcodeClaim(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID          string `json:"id"`
		PlanID      string `json:"planId"`
		Captcha     string `json:"captchaParam"`
		Region      string `json:"region"`
	}
	if err := readBody(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	acct := p.store.Get(in.ID)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	prov, ok := p.providerFor(store.PZCode).(*zcode.Provider)
	if !ok {
		writeErr(w, 500, "zcode 提供商未注册")
		return
	}
	name, already, err := prov.ClaimPlan(r.Context(), acct, in.PlanID, in.Captcha, in.Region)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	prov.ClearPendingClaim(acct.ID, in.PlanID) // 领取成功（含已领取过）→ 移出待办横幅
	_ = p.store.Mutate(acct.ID, func(a *store.Account) {
		a.LastCheckinAt = time.Now().Unix()
		if already {
			a.LastCheckinMsg = "已领取过 " + name
		} else {
			a.LastCheckinMsg = "已领取 " + name
		}
	})
	if !already {
		go p.afterAdd(acct)
	}
	writeJSON(w, map[string]any{"ok": true, "planName": name, "already": already})
}

// handleZcodePending 自动领取时撞到验证码、待人工滑块领取的活动列表。
func (p *Panel) handleZcodePending(w http.ResponseWriter, r *http.Request) {
	prov, ok := p.providerFor(store.PZCode).(*zcode.Provider)
	if !ok {
		writeJSON(w, map[string]any{"pending": []any{}})
		return
	}
	type item struct {
		AccountID   string `json:"accountId"`
		AccountName string `json:"accountName"`
		PlanID      string `json:"planId"`
		PlanName    string `json:"planName"`
	}
	out := []item{}
	for _, acct := range p.store.ByProvider(store.PZCode) {
		for _, pc := range prov.PendingClaims(acct.ID) {
			out = append(out, item{acct.ID, acct.DisplayedName(), pc.PlanID, pc.Name})
		}
	}
	writeJSON(w, map[string]any{"pending": out})
}

// handleZcodeCaptchaConfig 返回阿里云验证码配置（captchaId/scene/region 等），
// 前端据此渲染官方滑块，人工通过后把 captchaVerifyParam 传给 claim 重试。
func (p *Panel) handleZcodeCaptchaConfig(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	acct := p.store.Get(id)
	if acct == nil {
		writeErr(w, 404, "账号不存在")
		return
	}
	prov, ok := p.providerFor(store.PZCode).(*zcode.Provider)
	if !ok {
		writeErr(w, 500, "zcode 提供商未注册")
		return
	}
	cfg, err := prov.CaptchaConfig(r.Context(), acct)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"captcha": cfg})
}
