package localimport

import (
	"fmt"
	"os"
	"testing"
)

// TestWorkBuddyDecryptLive 门控：WB_LIVE=1 时对真实 auth 目录走完整链路
// （收集 keyId → 扫描运行中 WorkBuddy.exe 内存找回密钥 → AES-GCM 解密）。
// 只输出身份标识与 token 长度，不打印凭据明文。
func TestWorkBuddyDecryptLive(t *testing.T) {
	if os.Getenv("WB_LIVE") == "" {
		t.Skip("live probe only")
	}
	accts := WorkBuddyLocal()
	if len(accts) == 0 {
		t.Fatal("WorkBuddyLocal 返回空——解密链路失败或客户端未运行")
	}
	for _, a := range accts {
		fmt.Printf("realm=%s uid=%s nick=%s email=%s domain=%s\n  accessLen=%d refreshLen=%d exp=%d\n",
			a.Realm, a.UID, a.Nickname, a.Email, a.Domain,
			len(a.AccessToken), len(a.RefreshToken), a.ExpiresAt)
	}
}
