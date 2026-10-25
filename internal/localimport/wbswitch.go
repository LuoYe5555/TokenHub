// wb-switch 导出文件导入：把第三方 WorkBuddy 账号切换工具（wb-switch）导出的
// JSON 账号列表映射成 store.Account，供面板「导入 wb-switch 文件」使用。
//
// 导出格式（顶层裸数组，snake_case）：
//
//	[{ "access_token", "refresh_token", "uid", "nickname", "domain",
//	   "variant": "cn"|"intl", "expiresAt": <毫秒>, "refreshExpiresAt": <毫秒>,
//	   "auth_raw": {...}, "profile_raw": {...} }]
//
// 解析对键名大小写与命名风格宽容（access_token / accessToken / token 都认），
// 因此也能吃下网关内部 Account 形状与手工整理的单对象文件。
package localimport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"tokenhub/internal/logx"
	"tokenhub/internal/store"
)

// WBSwitchEntry 单条记录的解析结果（Account 为 nil 表示该条无效）。
type WBSwitchEntry struct {
	Index       int
	Account     *store.Account
	Err         string
	Warnings    []string
	AccessDead  bool // access_token 已过期（靠 refresh_token 续期）
	RefreshDead bool // refresh_token 也已过期，需重新登录
}

// WBSwitchItem 导入报告中的单条明细（不含凭据，可安全返回给前端）。
type WBSwitchItem struct {
	Index            int      `json:"index"`
	UID              string   `json:"uid"`
	Nickname         string   `json:"nickname"`
	Realm            string   `json:"realm"`
	Action           string   `json:"action"` // added | updated | skipped | invalid
	Reason           string   `json:"reason,omitempty"`
	ExpiresAt        int64    `json:"expiresAt,omitempty"`
	RefreshExpiresAt int64    `json:"refreshExpiresAt,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// WBSwitchReport 一次导入（或预演）的汇总。
type WBSwitchReport struct {
	DryRun  bool           `json:"dryRun"`
	Found   int            `json:"found"`
	Added   int            `json:"added"`
	Updated int            `json:"updated"`
	Skipped int            `json:"skipped"`
	Invalid int            `json:"invalid"`
	Items   []WBSwitchItem `json:"items"`
}

// ParseWBSwitchFile 解析 wb-switch 导出文件。
// 返回的 entries 与文件中的记录一一对应（含无效项，Err 非空）。
func ParseWBSwitchFile(data []byte) ([]*WBSwitchEntry, error) {
	rows, err := wbSwitchRows(data)
	if err != nil {
		return nil, err
	}
	entries := make([]*WBSwitchEntry, 0, len(rows))
	for i, raw := range rows {
		entries = append(entries, parseWBSwitchRow(i, raw))
	}
	return entries, nil
}

// wbSwitchRows 拆出记录数组：裸数组 / {"accounts":[...]} / 单对象 三种形态。
func wbSwitchRows(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("文件内容为空")
	}
	switch trimmed[0] {
	case '[':
		var rows []json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %w", err)
		}
		return rows, nil
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %w", err)
		}
		if accs, ok := obj["accounts"]; ok {
			var rows []json.RawMessage
			if err := json.Unmarshal(accs, &rows); err == nil {
				return rows, nil
			}
		}
		return []json.RawMessage{trimmed}, nil
	}
	return nil, fmt.Errorf("不是有效的 wb-switch 导出文件：顶层应为 JSON 数组")
}

// parseWBSwitchRow 把一条记录映射成 store.Account。
func parseWBSwitchRow(idx int, raw json.RawMessage) *WBSwitchEntry {
	e := &WBSwitchEntry{Index: idx}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil || row == nil {
		e.Err = "不是 JSON 对象"
		return e
	}
	auth := wbSubObject(row, "auth_raw", "authRaw", "auth")
	profile := wbSubObject(row, "profile_raw", "profileRaw", "profile")

	accessToken := firstNonEmpty(
		wbPickString(row, "access_token", "accessToken", "token"),
		wbPickString(auth, "accessToken", "access_token", "token"),
	)
	refreshToken := firstNonEmpty(
		wbPickString(row, "refresh_token", "refreshToken"),
		wbPickString(auth, "refreshToken", "refresh_token"),
	)
	if accessToken == "" && refreshToken == "" {
		e.Err = "缺少 access_token / refresh_token"
		return e
	}

	uid := firstNonEmpty(
		wbPickString(row, "uid", "userId", "user_id"),
		wbPickString(profile, "uid", "userId", "user_id"),
	)
	if uid == "" {
		uid = wbUIDFromJWT(accessToken)
		if uid == "" {
			uid = wbUIDFromJWT(refreshToken)
		}
		if uid != "" {
			e.Warnings = append(e.Warnings, "顶层缺少 uid，已从令牌载荷推断")
		}
	}
	if uid == "" {
		e.Err = "缺少 uid（账号唯一标识），无法去重导入"
		return e
	}
	if pu := wbPickString(profile, "uid"); pu != "" && pu != uid {
		e.Warnings = append(e.Warnings, "顶层 uid 与 profile_raw.uid 不一致，以顶层为准")
	}

	domain := firstNonEmpty(wbPickString(row, "domain"), wbPickString(auth, "domain"))
	realm := wbRealmOf(wbPickString(row, "variant", "realm", "region"), domain)

	nickname := firstNonEmpty(
		wbPickString(row, "nickname", "nickName", "name"),
		wbPickString(profile, "nickname", "nickName"),
		wbPickString(profile, "phoneNumber", "phone"),
		wbPickString(row, "email"),
	)

	// 时间戳：wb-switch 用毫秒；缺失时用 auth_raw 的 expiresIn（秒）配合 createdAt 推算
	createdAtMs := wbPickInt(row, "createdAt", "created_at")
	expiresAt := normalizeMs(wbPickInt(row, "expiresAt", "expires_at", "expiredAt"))
	if expiresAt <= 0 && createdAtMs > 0 {
		if in := wbPickInt(auth, "expiresIn"); in > 0 {
			expiresAt = normalizeMs(createdAtMs + in*1000)
		}
	}
	refreshExpiresAt := normalizeMs(wbPickInt(row, "refreshExpiresAt", "refresh_expires_at"))
	if refreshExpiresAt <= 0 && createdAtMs > 0 {
		if in := wbPickInt(auth, "refreshExpiresIn"); in > 0 {
			refreshExpiresAt = normalizeMs(createdAtMs + in*1000)
		}
	}

	now := time.Now().Unix()
	if expiresAt > 0 && expiresAt <= now {
		e.AccessDead = true
		e.Warnings = append(e.Warnings, "access_token 已过期，导入后将用 refresh_token 自动续期")
	}
	if refreshExpiresAt > 0 && refreshExpiresAt <= now {
		e.RefreshDead = true
		e.Warnings = append(e.Warnings, "refresh_token 也已过期（导出文件较旧），导入后需重新登录该账号")
	}
	if refreshToken == "" {
		e.Warnings = append(e.Warnings, "没有 refresh_token，access_token 过期后无法自动续期")
	}

	acct := &store.Account{
		Provider:     store.PWorkBuddy,
		Realm:        realm,
		UID:          uid,
		Nickname:     nickname,
		Email:        wbPickString(row, "email", "mail"),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		Domain:       domain,
		EnterpriseID: firstNonEmpty(
			wbPickString(row, "enterpriseId", "enterprise_id", "entId"),
			wbPickString(profile, "enterpriseId", "enterprise_id"),
		),
		Enabled:   true,
		CreatedAt: now,
	}
	acct.ExtraSet(store.ExtraWBImportSource, "wb-switch")
	if ss := wbPickString(auth, "sessionState", "session_state"); ss != "" {
		acct.ExtraSet(store.ExtraWBSessionState, ss)
	}
	if refreshExpiresAt > 0 {
		acct.ExtraSet(store.ExtraWBRefreshExpiresAt, fmt.Sprint(refreshExpiresAt))
	}
	if phone := wbPickString(profile, "phoneNumber", "phone"); phone != "" {
		acct.ExtraSet(store.ExtraWBPhone, phone)
	}
	if uin := wbPickString(profile, "uin"); uin != "" {
		acct.ExtraSet(store.ExtraWBUin, uin)
	}
	e.Account = acct
	return e
}

// ApplyWBSwitch 把解析结果写入账号库（dryRun 时只统计不落盘）。
// 去重按 UID：已存在同 UID 的 WorkBuddy 账号则更新凭据，否则新增。
func ApplyWBSwitch(st *store.Store, entries []*WBSwitchEntry, dryRun bool) *WBSwitchReport {
	rep := &WBSwitchReport{DryRun: dryRun, Found: len(entries)}
	byUID := map[string]*store.Account{}
	for _, a := range st.ByProvider(store.PWorkBuddy) {
		if a.UID != "" {
			if _, ok := byUID[a.UID]; !ok {
				byUID[a.UID] = a
			}
		}
	}

	for _, e := range entries {
		item := WBSwitchItem{
			Index:    e.Index,
			Reason:   e.Err,
			Warnings: e.Warnings,
			Action:   "invalid",
		}
		if e.Account == nil {
			rep.Invalid++
			rep.Items = append(rep.Items, item)
			continue
		}
		item.UID = e.Account.UID
		item.Nickname = e.Account.DisplayedName()
		item.Realm = e.Account.Realm
		item.ExpiresAt = e.Account.ExpiresAt
		if v := e.Account.ExtraGet(store.ExtraWBRefreshExpiresAt); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				item.RefreshExpiresAt = n
			}
		}

		old := byUID[e.Account.UID]
		if old == nil {
			item.Action = "added"
			rep.Added++
			if !dryRun {
				e.Account.ID = store.NewID()
				if err := st.Upsert(e.Account); err != nil {
					item.Action = "invalid"
					item.Reason = "写入失败: " + err.Error()
					rep.Added--
					rep.Invalid++
					rep.Items = append(rep.Items, item)
					continue
				}
				byUID[e.Account.UID] = e.Account
				logx.Infof("import", "wb-switch 新增 %s 账号: %s", e.Account.Realm, e.Account.DisplayedName())
			}
			rep.Items = append(rep.Items, item)
			continue
		}

		// 已存在同 UID：凭据无变化则跳过，否则原地更新（保留 ID 与用户设置）
		if old.AccessToken == e.Account.AccessToken && old.RefreshToken == e.Account.RefreshToken && !old.Dead {
			item.Action = "skipped"
			item.Reason = "该账号已在列表中且凭据相同"
			rep.Skipped++
			rep.Items = append(rep.Items, item)
			continue
		}
		item.Action = "updated"
		rep.Updated++
		if !dryRun {
			e.Account.ID = old.ID
			e.Account.Enabled = old.Enabled
			e.Account.CreatedAt = old.CreatedAt
			e.Account.LastCheckinDay = old.LastCheckinDay
			e.Account.LastCheckinAt = old.LastCheckinAt
			e.Account.LastCheckinMsg = old.LastCheckinMsg
			e.Account.LastQuota = old.LastQuota
			// 新凭据视为有效：清掉此前的失效/冷却状态
			e.Account.Dead = false
			e.Account.CooldownUntil = 0
			if err := st.Upsert(e.Account); err != nil {
				item.Action = "invalid"
				item.Reason = "写入失败: " + err.Error()
				rep.Updated--
				rep.Invalid++
				rep.Items = append(rep.Items, item)
				continue
			}
			byUID[e.Account.UID] = e.Account
			logx.Infof("import", "wb-switch 更新 %s 账号: %s", e.Account.Realm, e.Account.DisplayedName())
		}
		rep.Items = append(rep.Items, item)
	}
	return rep
}

// ── 取值助手（对键名大小写 / 下划线风格宽容） ──

func wbSubObject(m map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	for _, k := range keys {
		if raw, ok := m[k]; ok && len(raw) > 0 && raw[0] == '{' {
			var sub map[string]json.RawMessage
			if json.Unmarshal(raw, &sub) == nil {
				return sub
			}
		}
	}
	return nil
}

func wbPickString(m map[string]json.RawMessage, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

func wbPickInt(m map[string]json.RawMessage, keys ...string) int64 {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var n int64
		if json.Unmarshal(raw, &n) == nil {
			return n
		}
		// 少数导出器把时间写成字符串
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

// wbRealmOf variant / 域名 → 网关 realm（cn | global）。
func wbRealmOf(variant, domain string) string {
	switch strings.ToLower(strings.TrimSpace(variant)) {
	case "intl", "global", "int", "oversea", "international":
		return "global"
	case "cn", "china", "domestic", "mainland":
		return "cn"
	}
	d := strings.ToLower(domain)
	if strings.Contains(d, "workbuddy.ai") || strings.Contains(d, "codebuddy.ai") {
		return "global"
	}
	return "cn"
}

// wbUIDFromJWT 从 JWT 载荷推断用户 ID（sub / uid / data.uid / data.id）。
func wbUIDFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		if raw, err = base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return ""
		}
	}
	var payload struct {
		Sub  string `json:"sub"`
		UID  string `json:"uid"`
		Data struct {
			UID string `json:"uid"`
			ID  any    `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if s := strings.TrimSpace(payload.Sub); s != "" {
		return s
	}
	if s := strings.TrimSpace(payload.UID); s != "" {
		return s
	}
	if s := strings.TrimSpace(payload.Data.UID); s != "" {
		return s
	}
	if payload.Data.ID != nil {
		return fmt.Sprint(payload.Data.ID)
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
