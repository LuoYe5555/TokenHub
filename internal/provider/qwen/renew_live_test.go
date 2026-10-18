package qwen

// 千问续期链路的 live 测试（需要真实浏览器配置目录与网络）：
//
//	QWEN_RENEW_LIVE=1 go test ./internal/provider/qwen/ -run TestRenewFromProfileLive -v
//
// 仅在显式设置环境变量时执行；成功就地更新内存中的账号对象（不落库）。
import (
	"context"

	"os"
	"testing"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/store"
)

func TestRenewFromProfileLive(t *testing.T) {
	if os.Getenv("QWEN_RENEW_LIVE") == "" {
		t.Skip("live test; set QWEN_RENEW_LIVE=1 to run")
	}
	// profileDir 是相对 CWD 的 "data/qwen-profile"，测试里切到仓库根
	if err := os.Chdir("../../.."); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load("data")
	if err != nil {
		t.Fatal(err)
	}
	var acct *store.Account
	for _, a := range st.All() {
		if a.Provider == store.PQwen {
			acct = a
			break
		}
	}
	if acct == nil {
		t.Fatal("no qwen account")
	}
	old := acct.AccessToken
	if len(old) > 16 {
		t.Logf("old token prefix: %s...", old[:16])
	}
	p := New(config.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = p.Refresh(ctx, acct)
	if err != nil {
		t.Logf("Refresh error (可能令牌与 cookie 都过期): %v", err)
	}
	if acct.AccessToken != old {
		t.Logf("✅ 令牌已续期: %s...", acct.AccessToken[:16])
		if err := st.Upsert(acct); err != nil {
			t.Fatalf("落库失败: %v", err)
		}
		t.Log("已落库")
	} else {
		t.Logf("令牌未变化（cookie 与账号同值或续期失败）")
	}
}
