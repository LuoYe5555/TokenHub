package zcode

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// 诊断用：QWEN 不适用，环境开关 ZCODE_DIAG=1 时执行。
// 打印本机客户端凭据与账号库 token 的指纹对比，用于 3007 排查。
func TestDiagLocalCreds(t *testing.T) {
	if os.Getenv("ZCODE_DIAG") != "1" {
		t.Skip("set ZCODE_DIAG=1")
	}
	c, ok, err := ReadLocal()
	if err != nil || !ok {
		t.Fatalf("ReadLocal: ok=%v err=%v", ok, err)
	}
	h := sha256.Sum256([]byte(c.JWT))
	t.Logf("本机客户端 JWT len=%d sha16=%s uid=%s user=%s active=%s",
		len(c.JWT), hex.EncodeToString(h[:])[:16], c.UID, c.DisplayName, c.Active)
}
