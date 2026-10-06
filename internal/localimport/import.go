// Package localimport 从本机已安装客户端读取已登录账号（仅读取自有凭据，不外传）。
package localimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tokenhub/internal/logx"
	"tokenhub/internal/store"
)

// AddAllUnique 把本机读到的账号并入仓库（按 provider+uid 去重），返回新增数。
func AddAllUnique(st *store.Store, provider string, accounts []*store.Account) int {
	existing := map[string]bool{}
	for _, a := range st.ByProvider(provider) {
		if a.UID != "" {
			existing[a.UID] = true
		}
	}
	added := 0
	for _, a := range accounts {
		if a.UID == "" || existing[a.UID] {
			continue
		}
		a.Provider = provider
		a.Enabled = true
		a.CreatedAt = time.Now().Unix()
		if a.ID == "" {
			a.ID = store.NewID()
		}
		if err := st.Upsert(a); err == nil {
			existing[a.UID] = true
			added++
			logx.Infof("import", "本机导入 %s 账号: %s", provider, a.DisplayedName())
		}
	}
	return added
}

// ── WorkBuddy / CodeBuddy（明文 .info 会话文件） ──

func workbuddyAuthDir() string {
	if v := os.Getenv("TOKENHUB_WB_AUTH_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(local, "CodeBuddyExtension", "Data", "Public", "auth")
}

// WorkBuddyLocal 扫描本机会话文件（当前 + 历史会话）。
func WorkBuddyLocal() []*store.Account {
	dir := workbuddyAuthDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []*store.Account
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".info") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var session struct {
			Account struct {
				UID          string `json:"uid"`
				Nickname     string `json:"nickname"`
				EnterpriseID string `json:"enterpriseId"`
				Email        string `json:"email"`
			} `json:"account"`
			Auth struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresAt    int64  `json:"expiresAt"`
				Domain       string `json:"domain"`
			} `json:"auth"`
		}
		if json.Unmarshal(b, &session) != nil {
			continue
		}
		if strings.TrimSpace(session.Auth.AccessToken) == "" {
			continue
		}
		uid := session.Account.UID
		if uid == "" {
			continue
		}
		if seen[uid] {
			continue
		}
		seen[uid] = true
		realm := "cn"
		domain := strings.ToLower(session.Auth.Domain)
		if strings.Contains(domain, "workbuddy.ai") || strings.Contains(domain, "codebuddy.ai") {
			realm = "global"
		}
		acct := &store.Account{
			Realm:        realm,
			UID:          uid,
			Nickname:     session.Account.Nickname,
			Email:        session.Account.Email,
			AccessToken:  session.Auth.AccessToken,
			RefreshToken: session.Auth.RefreshToken,
			Domain:       session.Auth.Domain,
			EnterpriseID: session.Account.EnterpriseID,
		}
		if exp := normalizeMs(session.Auth.ExpiresAt); exp > 0 {
			acct.ExpiresAt = exp
		}
		out = append(out, acct)
	}
	return out
}

func normalizeMs(v int64) int64 {
	if v > 1e12 {
		return v / 1000
	}
	return v
}

// ── Trae（storage.json + tc 信封解密） ──

var traeAppNames = []string{"TRAE SOLO CN", "Trae CN", "TRAE SOLO", "Trae"}

const traeAuthKey = "iCubeAuthInfo://icube.cloudide"
const traeDCPrefix = "iCubeAuthInfo://icube-dc:"

func traeDataDir() string {
	if v := os.Getenv("TRAE_HOME"); v != "" {
		if st, err := os.Stat(filepath.Join(v, "User", "globalStorage", "storage.json")); err == nil && !st.IsDir() {
			return v
		}
		return ""
	}
	root := os.Getenv("APPDATA")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, "AppData", "Roaming")
	}
	for _, name := range traeAppNames {
		p := filepath.Join(root, name, "User", "globalStorage", "storage.json")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return filepath.Dir(filepath.Dir(filepath.Dir(p)))
		}
	}
	return ""
}

// TraeDirForLog 返回本机 Trae 数据目录（面板报错提示用）。
func TraeDirForLog() string { return traeDataDir() }

// TraeLocal 读取 Trae 客户端当前登录。注意：故意不导入 refreshToken——
// Trae 的刷新会轮转并作废 IDE 自己持有的 refreshToken，等于把用户正在用的 Trae 踢下线。
func TraeLocal() []*store.Account {
	dir := traeDataDir()
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "User", "globalStorage", "storage.json"))
	if err != nil {
		return nil
	}
	var storage map[string]any
	if json.Unmarshal(b, &storage) != nil {
		return nil
	}
	authVal, _ := storage[traeAuthKey].(string)
	auth := tcDecrypt(authVal)
	if auth == nil || auth.Token == "" {
		return nil
	}
	acct := &store.Account{
		Realm:       "cn",
		AccessToken: auth.Token,
		Nickname:    auth.Account.Username,
		Email:       auth.Account.Email,
	}
	acct.ExpiresAt = expiredAtUnix(auth.ExpiredAt)
	if uid := uidString(auth.UserID); uid != "" {
		acct.UID = uid
	}
	if acct.UID == "" {
		acct.UID = uidFromTraeJWT(auth.Token)
	}
	if did := traeDeviceID(storage); did != "" {
		acct.ExtraSet(store.ExtraTraeDeviceID, did)
	}
	if strings.HasPrefix(auth.Host, "http") {
		acct.ExtraSet(store.ExtraTraeAPIHost, auth.Host)
	}
	// 抓取登录信封原文（tc 加密串）：一键切换本机 Trae 登录时原样回写，会话不丢
	if envAuth, envEnt, envSrv := TraeEnvelopes(storage); envAuth != "" {
		acct.ExtraSet(store.ExtraTraeAuthEnvelope, envAuth)
		if envEnt != "" {
			acct.ExtraSet(store.ExtraTraeEntEnvelope, envEnt)
		}
		if envSrv != "" {
			acct.ExtraSet(store.ExtraTraeServerEnvelope, envSrv)
		}
	}
	return []*store.Account{acct}
}
