package tunnel

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestBoreE2E 对接真实 bore.pub 中继的端到端测试（需要外网）。
// 验证：握手拿公网端口 → 公网地址访问 → 转发回本地 HTTP 服务 → Stop 清理。
func TestBoreE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过外网测试")
	}
	// 本地假 HTTP 服务
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	localPort := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "HELLO-FROM-LOCAL")
	}))

	m := New(localPort)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// 等隧道就绪
	var url string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, u, em := m.Status()
		if st == StateUp {
			url = u
			break
		}
		if st == StateError {
			t.Fatalf("隧道出错: %s", em)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if url == "" {
		t.Fatal("20 秒内隧道未就绪")
	}
	t.Logf("公网地址: %s", url)

	// 通过公网地址回环访问本地服务
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url + "/")
	if err != nil {
		t.Fatalf("公网访问失败: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	t.Logf("HTTP %d 响应: %s", resp.StatusCode, string(buf[:n]))
	if resp.StatusCode != 200 || string(buf[:n]) != "HELLO-FROM-LOCAL" {
		t.Fatalf("响应异常: %d %q", resp.StatusCode, string(buf[:n]))
	}
}
