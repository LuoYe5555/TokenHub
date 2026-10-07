package localimport

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Trae 自研「tc」信封解密（逆向自 out/vs/base/common/byteCrypto.js，移植自 CreditDaddy traeLocal.js）：
// 6 字节头 + 32 字节随机盐，pepper 为随安装包分发的公开常量表（混淆而非加密），
// key/iv = SHA512(SHA512(salt) || pepper) 前 32B，AES-128-CBC，明文前 64B 为 body 的 SHA512 校验值。

var tcHeader = []byte{116, 99, 5, 16, 0, 0}

var woe = []byte{82, 9, 106, 213, 48, 54, 165, 56, 191, 64, 163, 158, 129, 243, 215, 251, 124, 227, 57, 130, 155, 47, 255, 135, 52, 142, 67, 68, 196, 222, 233, 203, 84, 123, 148, 50, 166, 194, 35, 61, 238, 76, 149, 11, 66, 250, 195, 78, 8, 46, 161, 102, 40, 217, 36, 178, 118, 91, 162, 73, 109, 139, 209, 37}
var voe = []byte{31, 221, 168, 51, 136, 7, 199, 49, 177, 18, 16, 89, 39, 128, 236, 95, 96, 81, 127, 169, 25, 181, 74, 13, 45, 229, 122, 159, 147, 201, 156, 239, 160, 224, 59, 77, 174, 42, 245, 176, 200, 235, 187, 60, 131, 83, 153, 97, 23, 43, 4, 126, 186, 119, 214, 38, 225, 105, 20, 99, 85, 33, 12, 125}
var joe = []byte{191, 192, 216, 250, 122, 246, 220, 97, 31, 254, 98, 27, 8, 72, 71, 176, 135, 99, 96, 18, 127, 101, 203, 104, 211, 102, 191, 125, 37, 72, 150, 156, 51, 229, 121, 35, 17, 153, 141, 177, 110, 131, 150, 128, 172, 255, 254, 6, 18, 140, 55, 62, 236, 249, 135, 64, 135, 12, 117, 4, 89, 149, 168, 209}
var hoe = []byte{246, 204, 26, 232, 232, 70, 129, 109, 223, 146, 169, 242, 23, 241, 105, 145, 50, 196, 165, 42, 254, 120, 3, 54, 244, 207, 209, 85, 53, 6, 138, 106, 175, 148, 31, 204, 186, 186, 165, 182, 87, 142, 49, 10, 39, 110, 26, 154, 86, 56, 173, 125, 18, 64, 198, 225, 99, 99, 83, 82, 191, 134, 76, 170}

func xorPepper(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// traeAuth tc 信封的明文结构。注意 expiredAt 新版是 RFC3339 字符串、旧版可能是毫秒时间戳，
// userId 可能是大整数（超 float64 精度风险用 any + 转换处理）。
type traeAuth struct {
	Token     string `json:"token"`
	UserID    any    `json:"userId"`
	ExpiredAt any    `json:"expiredAt"`
	Host      string `json:"host"`
	Account   struct {
		Username  string `json:"username"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	} `json:"account"`
	UserRegion struct {
		Region string `json:"region"`
	} `json:"userRegion"`
}

// uidString userId → 稳定字符串（大整数不能用 float64 %v，会变科学计数法）。
func uidString(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return n
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// expiredAtUnix expiredAt → unix 秒（RFC3339 字符串或毫秒/秒时间戳）。
func expiredAtUnix(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case string:
		if t, err := time.Parse(time.RFC3339, n); err == nil {
			return t.Unix()
		}
		var i int64
		fmt.Sscanf(strings.TrimSpace(n), "%d", &i)
		return normalizeMs(i)
	case float64:
		return normalizeMs(int64(n))
	case int:
		return normalizeMs(int64(n))
	case int64:
		return normalizeMs(n)
	}
	return 0
}

// TraeAuthInfo 导出的信封解析结果（诊断/一键切换用）。
type TraeAuthInfo struct {
	Token string
	UID   string
	Host  string
}

// TraeEnvelopeInfo 解开 tc 信封为导出结构；失败返回 nil。
func TraeEnvelopeInfo(b64 string) *TraeAuthInfo {
	a := tcDecrypt(b64)
	if a == nil {
		return nil
	}
	return &TraeAuthInfo{Token: a.Token, UID: uidString(a.UserID), Host: a.Host}
}

// TcDecryptForDiag 导出解密入口（诊断用）。
func TcDecryptForDiag(b64 string) *traeAuth { return tcDecrypt(b64) }

// tcDecrypt 解开 storage.json 里的 tc 信封；失败返回 nil。
func tcDecrypt(b64 string) *traeAuth {
	if b64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		// 尝试 URL-safe
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil
		}
	}
	if len(raw) < 54 || string(raw[:6]) != string(tcHeader) {
		return nil
	}
	salt := raw[6:38]
	cipherText := raw[38:]
	for _, pepper := range [][]byte{xorPepper(woe, voe), xorPepper(joe, hoe)} {
		saltSum := sha512.Sum512(salt)
		sum := sha512.Sum512(append(saltSum[:], pepper...))
		block, err := aes.NewCipher(sum[:16])
		if err != nil {
			continue
		}
		mode := cipher.NewCBCDecrypter(block, sum[16:32])
		plain := make([]byte, len(cipherText))
		mode.CryptBlocks(plain, cipherText)
		plain = pkcs7Unpad(plain, 16)
		if len(plain) <= 64 {
			continue
		}
		body := plain[64:]
		check := sha512.Sum512(body)
		if string(check[:]) != string(plain[:64]) {
			continue
		}
		var auth traeAuth
		if json.Unmarshal(body, &auth) != nil {
			continue
		}
		return &auth
	}
	return nil
}

func pkcs7Unpad(data []byte, blockSize int) []byte {
	if len(data) == 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return data
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return data
		}
	}
	return data[:len(data)-pad]
}

// Trae storage.json 登录相关键（与客户端约定，切换登录时回写用）。
const (
	TraeAuthKeyX   = "iCubeAuthInfo://icube.cloudide"
	TraeEntKeyX    = "iCubeEntitlementInfo://icube.cloudide"
	TraeServerKeyX = "iCubeServerData://icube.cloudide"
	TraeUsertagKey = "iCubeAuthInfo://usertag"
)

// TraeEnvelopes 从 storage.json 抓取三个登录信封原文（tc 加密串，切换登录时原样回写）。
// 缺失的键返回空串。
func TraeEnvelopes(storage map[string]any) (auth, ent, server string) {
	auth, _ = storage[TraeAuthKeyX].(string)
	ent, _ = storage[TraeEntKeyX].(string)
	server, _ = storage[TraeServerKeyX].(string)
	return
}

// tcSeal tcDecrypt 的对称加密（写回 IDE 登录用）：随机 32B 盐 + 头，
// key/iv = SHA512(SHA512(salt) || pepper)，AES-128-CBC，明文前 64B 为 body 的 SHA512。
// pepper 用 AES 档（woe^voe），与客户端 encryptBytes 一致。
func tcSeal(plain []byte) (string, error) {
	salt := make([]byte, 32)
	if _, err := crand.Read(salt); err != nil {
		return "", err
	}
	h1 := sha512.Sum512(salt)
	buf := append(append([]byte{}, h1[:]...), xorPepper(woe, voe)...)
	h2 := sha512.Sum512(buf)
	block, err := aes.NewCipher(h2[:16])
	if err != nil {
		return "", err
	}
	body := make([]byte, 64+len(plain))
	check := sha512.Sum512(plain)
	copy(body, check[:])
	copy(body[64:], plain)
	pad := 16 - len(body)%16
	padded := append(body, make([]byte, pad)...)
	for i := len(body); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, 0, 6+32+len(padded))
	out = append(out, tcHeader...)
	out = append(out, salt...)
	mode := cipher.NewCBCEncrypter(block, h2[16:32])
	cipherText := make([]byte, len(padded))
	mode.CryptBlocks(cipherText, padded)
	out = append(out, cipherText...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// SynthAuthEnvelope 用已知凭据合成 tc 登录信封（无原始信封的账号一键切换时兜底）。
func SynthAuthEnvelope(token, refreshToken, uid, nickname, email, host string) (string, error) {
	if host == "" {
		host = "https://api.trae.com.cn"
	}
	auth := map[string]any{
		"token":          token,
		"refreshToken":   refreshToken,
		"expiredAt":      time.Now().Add(14 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"),
		"refreshExpiredAt": time.Now().Add(180 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"),
		"tokenReleaseAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"userId":         uid,
		"host":           host,
		"userRegion":     map[string]any{"region": "CN", "_aiRegion": "CN"},
		"account": map[string]any{
			"username": nickname, "iss": "", "iat": 0, "organization": "",
			"work_country": "", "email": email, "avatar_url": "",
			"description": "", "scope": "marscode", "loginScope": "trae",
			"storeCountryCode": "cn", "storeCountrySrc": "uid",
			"storeRegion": "CN", "userTag": "row",
		},
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		return "", err
	}
	return tcSeal(raw)
}

// SynthEntEnvelope 合成 Free 档权益信封（与同类工具 buildEntitlementInfo 一致）。
func SynthEntEnvelope() (string, error) {
	ent := map[string]any{
		"identityStr": "Free", "identity": 0, "isPayFreshman": false,
		"isSupportCommercialization": true, "hasPackage": false, "enableEntitlement": true,
		"detail": map[string]any{
			"can_gen_solo_code": false, "fast_request_per": 1, "in_wait": false,
			"permission": 1, "toast_read": false, "toastRead": false,
			"canGenSoloCode": false, "fastRequestPer": 1, "inWaitlist": false,
		},
	}
	raw, err := json.Marshal(ent)
	if err != nil {
		return "", err
	}
	return tcSeal(raw)
}

// traeDeviceID 客户端自报设备号（iCubeAuthInfo://icube-dc:<纯数字>），签到风控要求纯数字。
func traeDeviceID(storage map[string]any) string {
	for key := range storage {
		if !strings.HasPrefix(key, traeDCPrefix) {
			continue
		}
		id := strings.TrimPrefix(key, traeDCPrefix)
		if len(id) >= 8 && isAllDigits(id) {
			return id
		}
	}
	return ""
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// uidFromTraeJWT Cloud-IDE-JWT 的 payload.data.id 即 uid。
func uidFromTraeJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var payload struct {
		Data struct {
			ID any `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if payload.Data.ID == nil {
		return ""
	}
	return fmt.Sprintf("%v", payload.Data.ID)
}
