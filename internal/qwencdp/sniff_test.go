package qwencdp

// 抓真前端的自刷新端点：headless 打开 chat.qwen.ai，监听 Network 事件，
// 打印所有含 auth/refresh/token/sign 的请求（live，QWEN_SNIFF=1）。
import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSniffAuthEndpoints(t *testing.T) {
	if os.Getenv("QWEN_SNIFF") == "" {
		t.Skip("live test; set QWEN_SNIFF=1")
	}
	_ = os.Chdir("../..")
	profile, _ := filepath.Abs(filepath.Join("data", "qwen-profile"))
	exe := FindBrowser()
	if exe == "" {
		t.Fatal("no browser")
	}
	port, _ := FreePort()
	cmd, err := Launch(exe, profile, port, "https://chat.qwen.ai/", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if !WaitDebug(port, 25*time.Second) {
		t.Fatal("debug timeout")
	}
	time.Sleep(3 * time.Second)

	// 找 page target
	var targets []struct {
		Type                 string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		URL                  string `json:"url"`
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json", port))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&targets)
	resp.Body.Close()
	var pageWS string
	for _, tg := range targets {
		if tg.Type == "page" && strings.Contains(tg.URL, "qwen.ai") {
			pageWS = tg.WebSocketDebuggerURL
			break
		}
	}
	if pageWS == "" {
		for _, tg := range targets {
			if tg.Type == "page" {
				pageWS = tg.WebSocketDebuggerURL
			}
		}
	}
	if pageWS == "" {
		t.Fatal("no page target")
	}
	t.Logf("page target: %s", pageWS)

	ws, err := wsDial(pageWS)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if _, err := ws.Call(1, "Network.enable", map[string]any{}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	_, _ = ws.Call(2, "Page.enable", map[string]any{}, 10*time.Second)
	_, _ = ws.Call(3, "Page.reload", map[string]any{}, 10*time.Second)
	t.Log("reloading, sniffing 25s...")
	deadline := time.Now().Add(25 * time.Second)
	ws.c.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		data, err := ws.readMessage()
		if err != nil {
			t.Logf("read end: %v", err)
			return
		}
		var ev struct {
			Method string `json:"method"`
			Params struct {
				Request struct {
					URL      string            `json:"url"`
					Method   string            `json:"method"`
					PostData string            `json:"postData"`
					Headers  map[string]string `json:"headers"`
				} `json:"request"`
			} `json:"params"`
		}
		if json.Unmarshal(data, &ev) != nil || ev.Method != "Network.requestWillBeSent" {
			continue
		}
		u := ev.Params.Request.URL
		lu := strings.ToLower(u)
		if !strings.Contains(lu, "qwen.ai") || strings.HasSuffix(strings.SplitN(u, "?", 2)[0], ".js") || strings.HasSuffix(strings.SplitN(u, "?", 2)[0], ".css") || strings.HasSuffix(strings.SplitN(u, "?", 2)[0], ".png") || strings.HasSuffix(strings.SplitN(u, "?", 2)[0], ".woff2") {
			continue
		}
		t.Logf("%s %s", ev.Params.Request.Method, u)
		if strings.Contains(lu, "chat.qwen.ai/api/") {
			h := ev.Params.Request.Headers
			if auth := h["Authorization"]; auth != "" {
				t.Logf("   AUTH: %s…", trunc(auth, 40))
			} else {
				t.Logf("   AUTH: (none)")
			}
			if ck := h["Cookie"]; ck != "" {
				names := []string{}
				for _, kv := range strings.Split(ck, "; ") {
					names = append(names, strings.SplitN(kv, "=", 2)[0])
				}
				t.Logf("   COOKIES: %s", strings.Join(names, ","))
			}
		}
		if strings.Contains(lu, "auths/refresh") || strings.Contains(lu, "auths/") {
			for k, v := range ev.Params.Request.Headers {
				lk := strings.ToLower(k)
				if lk == "cookie" {
					v = trunc(v, 60) + "…"
				}
				t.Logf("   H %s: %s", k, trunc(v, 160))
			}
		}
		if pd := ev.Params.Request.PostData; pd != "" {
			t.Logf("   body: %s", trunc(pd, 200))
		}
	}
}
