package qwencdp

// 无头续期链路分步探针（live）：
//
//	QWEN_LIVE=1 go test ./internal/qwencdp/ -run TestLaunchProbe -v
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchProbe(t *testing.T) {
	if os.Getenv("QWEN_LIVE") == "" {
		t.Skip("live test; set QWEN_LIVE=1")
	}
	_ = os.Chdir("../..")
	profile, _ := filepath.Abs(filepath.Join("data", "qwen-profile"))
	t.Logf("1. profile=%s hasCookies=%v", profile, ProfileHasCookies(profile))
	exe := FindBrowser()
	t.Logf("2. browser=%q", exe)
	if exe == "" {
		t.Fatal("no browser found")
	}
	port, err := FreePort()
	t.Logf("3. port=%d err=%v", port, err)
	cmd, err := Launch(exe, profile, port, "https://chat.qwen.ai/", true)
	if err != nil {
		t.Fatalf("4. launch failed: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	t.Logf("4. launched pid=%d", cmd.Process.Pid)
	ok := WaitDebug(port, 25*time.Second)
	t.Logf("5. debug ready=%v", ok)
	if !ok {
		return
	}
	cookies, err := GetCookies(port)
	t.Logf("6. cookies n=%d err=%v", len(cookies), err)
	for _, c := range cookies {
		if !containsFold(c.Domain, "qwen.ai") {
			continue
		}
		t.Logf("   cookie name=%q len=%d domain=%s prefix=%q", c.Name, len(c.Value), c.Domain, trunc(c.Value, 30))
	}
	token, refresh := PickAuth(cookies)
	t.Logf("7. token=%q refresh=%q", trunc(token, 40), trunc(refresh, 40))
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
