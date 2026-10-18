package qwen

// 验证 WAF 假设：dump chats/new 的原始响应体（Go TLS 指纹下到底是什么）。
import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/store"
)

func TestChatsNewRawDump(t *testing.T) {
	if os.Getenv("QWEN_RENEW_LIVE") == "" {
		t.Skip("live test")
	}
	_ = os.Chdir("../../..")
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
	p := New(config.Default())
	base := p.base()
	// 原子链路：先换新 token，紧接着建会话
	if res, err := ExchangeRefresh(context.Background(), base, acct.RefreshToken); err == nil && res.Token != "" {
		acct.AccessToken = res.Token
		t.Logf("exchanged ok: %s… (refresh rotated: %v)", res.Token[:20], res.RefreshToken != "")
	} else if err != nil {
		t.Logf("exchange failed: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"title": "Probe", "models": []string{"qwen3.8-max"},
		"chat_mode": "normal", "chat_type": "t2t",
		"timestamp": time.Now().UnixMilli(), "urf": "", "project_id": "",
	})
	resp, err := httpx.Do(context.Background(), "POST", base+"/api/v2/chats/new",
		headers(acct, base, ""), payload, 30*time.Second)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Logf("status=%d contentType=%s", resp.Status, resp.ContentType)
	body := string(resp.Body)
	if len(body) > 800 {
		body = body[:800]
	}
	t.Logf("body=%s", strings.ReplaceAll(body, "\n", " "))
}
