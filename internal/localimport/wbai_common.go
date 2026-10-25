// WorkBuddy 会话字段的解析与解密（跨平台）。
//
// 新版客户端把 auth .info 里的 accessToken/refreshToken 加密成
// {"$wbEncrypted":1,"envelope":"..."} 信封（AES-256-GCM，suite 1，sym-v1 field framing）。
// 信封本身的解析与解密是纯 Go 的 crypto，任何平台都能跑；只有「从运行中的客户端
// 进程内存找回密钥」这一步依赖 Windows（见 wbai_windows.go）。
//
// 所以非 Windows 平台上：明文会话照常可读，加密会话因取不到密钥而跳过。
package localimport

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

// wbAAD 复刻 buildAuthenticatedContextAad 的 sym-v1 分支：
// "WB-AAD\0" || 0x01 || lenp(framingMagic) || lenp("sym-v1") || uint32BE(suite)
// || lenp(keyId) || [framing枚举] || 0x00(seq缺省) || 0x00(final缺省)
func wbAAD(keyID string, suite int) []byte {
	lenp := func(b string) []byte {
		out := make([]byte, 4, 4+len(b))
		binary.BigEndian.PutUint32(out, uint32(len(b)))
		return append(out, b...)
	}
	aad := []byte("WB-AAD\x00")
	aad = append(aad, 0x01)
	aad = append(aad, lenp(wbFieldFramingMagic)...)
	aad = append(aad, lenp("sym-v1")...)
	var s [4]byte
	binary.BigEndian.PutUint32(s[:], uint32(suite))
	aad = append(aad, s[:]...)
	aad = append(aad, lenp(keyID)...)
	aad = append(aad, 0x02) // framing 枚举 field=2
	aad = append(aad, 0x00, 0x00)
	return aad
}

// wbOpenEnvelope 用 key32 解一个 field 信封，返回明文字节。
func wbOpenEnvelope(key []byte, envRaw string) ([]byte, error) {
	inner, err := base64Decode(envRaw)
	if err != nil {
		return nil, fmt.Errorf("envelope base64: %w", err)
	}
	var env wbEnvelopeEnvelope
	if err := json.Unmarshal(inner, &env); err != nil {
		return nil, fmt.Errorf("envelope json: %w", err)
	}
	nonce, err := base64Decode(env.Nonce)
	if err != nil || len(nonce) != 12 {
		return nil, fmt.Errorf("nonce 非法")
	}
	tag, err := base64Decode(env.AuthTag)
	if err != nil || len(tag) != 16 {
		return nil, fmt.Errorf("authTag 非法")
	}
	ct, err := base64Decode(env.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("ciphertext base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, append(ct, tag...), wbAAD(env.KeyID, env.Suite))
	if err != nil {
		return nil, fmt.Errorf("GCM open: %w", err)
	}
	return plain, nil
}

func base64Decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty base64")
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// ── 字段解析助手（明文/信封双形态） ──

func wbAsEncrypted(raw json.RawMessage) (wbEncryptedField, bool) {
	var f wbEncryptedField
	if len(raw) == 0 || raw[0] != '{' {
		return f, false
	}
	if json.Unmarshal(raw, &f) != nil {
		return f, false
	}
	return f, f.WBEncrypted == 1 && f.Envelope != ""
}

func wbDecodeEnvelope(b64 string) (wbEnvelopeEnvelope, error) {
	inner, err := base64Decode(b64)
	if err != nil {
		return wbEnvelopeEnvelope{}, err
	}
	var env wbEnvelopeEnvelope
	err = json.Unmarshal(inner, &env)
	return env, err
}

func wbPlainString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func wbPlainInt(raw json.RawMessage) int64 {
	var n int64
	if json.Unmarshal(raw, &n) != nil {
		return 0
	}
	return n
}

// wbFieldString 把 auth/account 字段还原成字符串：明文直接返回；
// 加密信封用扫描到的密钥解密。第二个返回值表示该字段是否成功取得。
// keys 为 nil（非 Windows 或未找回密钥）时，只有明文字段能取到。
func wbFieldString(raw json.RawMessage, keys map[string][]byte) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	if raw[0] == '"' {
		return wbPlainString(raw), true
	}
	f, ok := wbAsEncrypted(raw)
	if !ok {
		return "", false
	}
	env, err := wbDecodeEnvelope(f.Envelope)
	if err != nil {
		return "", false
	}
	key := keys[env.KeyID]
	if key == nil {
		return "", false
	}
	plain, err := wbOpenEnvelope(key, f.Envelope)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(plain)), true
}

func wbFieldStringOr(raw json.RawMessage, keys map[string][]byte, def string) string {
	if s, ok := wbFieldString(raw, keys); ok {
		return s
	}
	return def
}
