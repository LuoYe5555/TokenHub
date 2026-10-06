package localimport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// tcEncrypt 测试用加密（与客户端信封格式互逆，首 pepper 模式）。
func tcEncrypt(t *testing.T, auth any) string {
	t.Helper()
	body, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	// 客户端信封：明文 = SHA512(body)(64B) || body
	bodySum := sha512.Sum512(body)
	plain := append(bodySum[:], body...)
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	pepper := xorPepper(woe, voe)
	saltSum := sha512.Sum512(salt)
	sum := sha512.Sum512(append(saltSum[:], pepper...))
	block, _ := aes.NewCipher(sum[:16])
	pad := 16 - len(plain)%16
	padded := append(append([]byte{}, plain...), make([]byte, pad)...)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	enc := cipher.NewCBCEncrypter(block, sum[16:32])
	out := make([]byte, len(padded))
	enc.CryptBlocks(out, padded)
	raw := append(append(append([]byte{}, tcHeader...), salt...), out...)
	return base64.StdEncoding.EncodeToString(raw)
}

func TestTcDecryptRoundTrip(t *testing.T) {
	orig := traeAuth{Token: "abc.def.ghi", UserID: 12345, ExpiredAt: 1786847930141, Host: "https://api.trae.com.cn"}
	orig.Account.Username = "测试用户"
	orig.Account.Email = "u@example.com"
	enc := tcEncrypt(t, orig)
	got := tcDecrypt(enc)
	if got == nil {
		t.Fatal("decrypt failed")
	}
	if got.Token != orig.Token || uidString(got.UserID) != "12345" || got.Account.Username != orig.Account.Username {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if expiredAtUnix(orig.ExpiredAt) != 1786847930141/1000 {
		t.Fatalf("expiredAt parse wrong: %v", expiredAtUnix(orig.ExpiredAt))
	}
	// RFC3339 字符串格式（新版 Trae）+ 字符串 userId
	orig2 := map[string]any{"token": "t2", "userId": "u-str", "expiredAt": "2026-10-19T10:03:53.796Z"}
	got2 := tcDecrypt(tcEncrypt(t, orig2))
	if got2 == nil {
		t.Fatal("decrypt v2 failed")
	}
	if uidString(got2.UserID) != "u-str" || expiredAtUnix(got2.ExpiredAt) == 0 {
		t.Fatalf("v2 parse mismatch: %+v", got2)
	}
	// 非法输入
	if tcDecrypt("") != nil || tcDecrypt("not-base64!!") != nil || tcDecrypt(base64.StdEncoding.EncodeToString([]byte("short"))) != nil {
		t.Fatal("expected nil for invalid input")
	}
}

func TestWorkBuddySessionParse(t *testing.T) {
	// WorkBuddyLocal 的目录由环境变量控制，解析逻辑在 import.go 里内联；
	// 这里只验证 realm 判定相关的字符串逻辑（与生产一致的字段名）。
	dir := t.TempDir()
	t.Setenv("TOKENHUB_WB_AUTH_DIR", dir)
	session := map[string]any{
		"account": map[string]any{"uid": "u777", "nickname": "WB本机", "enterpriseId": "ent9"},
		"auth": map[string]any{
			"accessToken": "at-1", "refreshToken": "rt-1",
			"expiresAt": int64(1786847930141), "domain": "www.workbuddy.ai",
		},
	}
	writeInfo(t, dir, "workbuddy-desktop.info", session)
	writeInfo(t, dir, "workbuddy-desktop.2026-10-01.12.abc.info", session) // 历史会话同 uid
	accts := WorkBuddyLocal()
	if len(accts) != 1 {
		t.Fatalf("want 1 unique account, got %d", len(accts))
	}
	a := accts[0]
	if a.UID != "u777" || a.Realm != "global" || a.AccessToken != "at-1" || a.ExpiresAt != 1786847930141/1000 {
		t.Fatalf("unexpected account: %+v", a)
	}
}

func writeInfo(t *testing.T, dir, name string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := osWriteFile(dir+"/"+name, b); err != nil {
		t.Fatal(err)
	}
}

// TestTcSealRoundTrip 生产加密 tcSeal 与 tcDecrypt 互逆（合成信封路径）。
func TestTcSealRoundTrip(t *testing.T) {
	plain := []byte(`{"token":"tok-3","userId":42,"host":"https://api.trae.com.cn"}`)
	enc, err := tcSeal(plain)
	if err != nil {
		t.Fatal(err)
	}
	got := tcDecrypt(enc)
	if got == nil {
		t.Fatal("tcSeal output not decryptable")
	}
	if got.Token != "tok-3" || uidString(got.UserID) != "42" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
