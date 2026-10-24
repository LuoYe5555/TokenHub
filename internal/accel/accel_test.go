package accel

import (
	"strings"
	"testing"

	"tokenhub/internal/config"
)

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{DataDir: t.TempDir()}
}

func TestIsAccelDomain(t *testing.T) {
	yes := []string{
		"workbuddy.ai", "www.workbuddy.ai", "auth.workbuddy.ai",
		"download.codebuddy.ai", "github.com", "raw.githubusercontent.com",
		"cdn.jsdelivr.net", "hf.co",
	}
	for _, d := range yes {
		if !isAccelDomain(d) {
			t.Errorf("应命中加速名单: %s", d)
		}
	}
	no := []string{
		"baidu.com", "www.qq.com", "notworkbuddy.ai", "evil-workbuddy.ai",
		"localhost", "192.168.1.1", "github.cn.example.com",
	}
	for _, d := range no {
		if isAccelDomain(d) {
			t.Errorf("不应命中加速名单: %s", d)
		}
	}
}

func TestPacScript(t *testing.T) {
	m := New(testCfg(t))
	m.backup = &proxyBackup{ProxyServer: "127.0.0.1:7890"}
	js := m.pacScript()
	for _, want := range []string{
		"function FindProxyForURL",
		"PROXY " + ListenAddr,
		"PROXY 127.0.0.1:7890", // 原全局代理串联
		"\"workbuddy.ai\"",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("PAC 缺少 %q", want)
		}
	}
	// 无原代理时兜底必须直连
	m.backup = nil
	if js := m.pacScript(); !strings.Contains(js, "return 'DIRECT'") {
		t.Error("无原代理时 PAC 兜底应为 DIRECT")
	}
}
