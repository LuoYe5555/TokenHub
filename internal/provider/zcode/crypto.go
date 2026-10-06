// Package zcode 智谱 ZCode（GLM Coding Plan / Start Plan）上游客户端。
// 协议移植自 CreditDaddy 与 zcodeReverseEngineering（开源）：Plan 渠道 Anthropic 格式。
package zcode

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"tokenhub/internal/store"
)

func randRead(b []byte) (int, error) { return rand.Read(b) }

const encPrefix = "enc:v1:"

// platformTag zcode 客户端使用的平台标识。
func platformTag() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return runtime.GOOS
	}
}

// DefaultSecret 凭据解密密钥来源（与客户端一致）：
// ZCODE_CREDENTIAL_SECRET 环境变量，否则 "zcode-credential-fallback:<platform>:<home>:<username>"。
func DefaultSecret(home string) string {
	if v := strings.TrimSpace(os.Getenv("ZCODE_CREDENTIAL_SECRET")); v != "" {
		return v
	}
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	if user == "" {
		user = "unknown"
	}
	return fmt.Sprintf("zcode-credential-fallback:%s:%s:%s", platformTag(), home, user)
}

func deriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// SafeDecrypt 值可能是明文或 enc:v1 密文，统一解为明文；失败返回空串。
func SafeDecrypt(value, secret string) string {
	if !strings.HasPrefix(value, encPrefix) {
		return value
	}
	parts := strings.SplitN(strings.TrimPrefix(value, encPrefix), ".", 3)
	if len(parts) != 3 {
		return ""
	}
	nonce, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	tag, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	ct, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || len(nonce) != 12 {
		return ""
	}
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	plain, err := gcm.Open(nil, nonce, append(tag, ct...), nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

// EncryptValue 加密为 enc:v1 格式（与客户端互逆）。
func EncryptValue(plain, secret string) string {
	block, _ := aes.NewCipher(deriveKey(secret))
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	_, _ = randRead(nonce)
	ct := gcm.Seal(nil, nonce, []byte(plain), nil)
	tag := ct[len(ct)-16:]
	body := ct[:len(ct)-16]
	return encPrefix + base64.RawURLEncoding.EncodeToString(nonce) + "." +
		base64.RawURLEncoding.EncodeToString(tag) + "." +
		base64.RawURLEncoding.EncodeToString(body)
}

// DecodeJWT 解出 JWT payload（不校验签名）。
func DecodeJWT(token string) map[string]any {
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

// ── 本机凭据导入 ──

// LocalPaths 本机 ZCode 客户端数据路径（支持 dataBaseDir 挪盘）。
type LocalPaths struct {
	Home        string
	Credentials string
	Config      string
}

// ResolveLocalPaths 定位 ~/.zcode/v2/credentials.json（dataBaseDir 挪盘感知）。
func ResolveLocalPaths() (*LocalPaths, bool) {
	home := os.Getenv("ZCODE_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		home = h
	}
	root := home
	var setting map[string]any
	if b, err := os.ReadFile(filepath.Join(home, ".zcode", "v2", "setting.json")); err == nil {
		_ = json.Unmarshal(b, &setting)
	}
	if db, ok := setting["dataBaseDir"].(string); ok && db != "" && filepath.IsAbs(db) {
		root = db
	}
	return &LocalPaths{
		Home:        home,
		Credentials: filepath.Join(root, ".zcode", "v2", "credentials.json"),
		Config:      filepath.Join(root, ".zcode", "v2", "config.json"),
	}, true
}

// LocalCreds 本机读取并解密后的凭据。
type LocalCreds struct {
	Raw          map[string]string // 全部键值（仍保留原 enc 密文）
	JWT          string            // zcodejwttoken
	Active       string            // oauth:active_provider
	BigmodelAT   string
	BigmodelRT   string
	ZaiAT        string
	UID          string
	Username     string
	DisplayName  string
	Email        string
	LoginProvider string
}

// ToAccount 把本机凭据转为账号记录（不含网络请求）。
func (c *LocalCreds) ToAccount() *store.Account {
	if c.JWT == "" && c.BigmodelAT == "" && c.ZaiAT == "" {
		return nil
	}
	raw, _ := json.Marshal(c.Raw)
	acct := &store.Account{
		Realm:       c.LoginProvider,
		UID:         c.UID,
		Nickname:    firstNonEmpty(c.DisplayName, c.Username),
		Email:       c.Email,
		AccessToken: c.JWT,
		Extra:       map[string]string{},
	}
	acct.Extra[store.ExtraZCodeCredentials] = string(raw)
	if c.BigmodelAT != "" {
		acct.Extra[store.ExtraZCodeBigmodelAT] = c.BigmodelAT
	}
	if c.BigmodelRT != "" {
		acct.Extra[store.ExtraZCodeBigmodelRT] = c.BigmodelRT
	}
	if c.ZaiAT != "" {
		acct.Extra[store.ExtraZCodeZaiAT] = c.ZaiAT
	}
	return acct
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ReadLocal 读取本机当前登录凭据；未登录返回 false。
func ReadLocal() (*LocalCreds, bool, error) {
	lp, ok := ResolveLocalPaths()
	if !ok {
		return nil, false, fmt.Errorf("无法定位用户目录")
	}
	b, err := os.ReadFile(lp.Credentials)
	if err != nil {
		return nil, false, fmt.Errorf("读取 %s 失败: %w", lp.Credentials, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false, fmt.Errorf("credentials.json 解析失败: %w", err)
	}
	secret := DefaultSecret(lp.Home)
	out := &LocalCreds{Raw: map[string]string{}}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			bb, _ := json.Marshal(v)
			s = string(bb)
		}
		out.Raw[k] = s
	}
	out.JWT = strings.TrimSpace(SafeDecrypt(out.Raw["zcodejwttoken"], secret))
	out.Active = SafeDecrypt(out.Raw["oauth:active_provider"], secret)
	if out.Active == "" {
		out.Active = "zai"
	}
	out.BigmodelAT = SafeDecrypt(out.Raw["oauth:bigmodel:access_token"], secret)
	out.BigmodelRT = SafeDecrypt(out.Raw["oauth:bigmodel:refresh_token"], secret)
	out.ZaiAT = SafeDecrypt(out.Raw["oauth:zai:access_token"], secret)

	// 身份：优先 user_info，退回解码 JWT
	uiJSON := SafeDecrypt(out.Raw["oauth:"+out.Active+":user_info"], secret)
	var ui map[string]any
	if json.Unmarshal([]byte(uiJSON), &ui) == nil && ui != nil {
		out.Username, _ = ui["username"].(string)
		out.DisplayName, _ = ui["displayName"].(string)
		out.Email, _ = ui["email"].(string)
		switch id := ui["id"].(type) {
		case string:
			out.UID = id
		case float64:
			out.UID = fmt.Sprintf("%v", id)
		}
	}
	if out.UID == "" {
		at := SafeDecrypt(out.Raw["oauth:"+out.Active+":access_token"], secret)
		if m := DecodeJWT(at); m != nil {
			if v, ok := m["user_id"]; ok {
				out.UID = fmt.Sprintf("%v", v)
			} else if v, ok := m["sub"]; ok {
				out.UID = fmt.Sprintf("%v", v)
			}
		}
	}
	if out.JWT == "" {
		// config.json 里可能存明文 coding-plan apiKey
		if cfg := readJSONFile(lp.Config); cfg != nil {
			if prov, ok := cfg["provider"].(map[string]any); ok {
				for id, pv := range prov {
					p, ok := pv.(map[string]any)
					if !ok {
						continue
					}
					if opts, ok := p["options"].(map[string]any); ok {
						if key, ok := opts["apiKey"].(string); ok && strings.Contains(id, "start-plan") && len(key) > 20 {
							out.JWT = key
						}
					}
				}
			}
		}
	}
	if out.JWT == "" && out.BigmodelAT == "" && out.ZaiAT == "" {
		return nil, false, nil
	}
	out.LoginProvider = out.Active
	return out, true, nil
}

func readJSONFile(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}
