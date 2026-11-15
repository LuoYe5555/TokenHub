package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestInstallBuiltinAndRPC(t *testing.T) {
	m := newTestManager(t)
	shelf := m.Shelf()
	if len(shelf) == 0 {
		t.Fatal("内置货架为空")
	}
	man, err := m.InstallBuiltin(shelf[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if man.ID != "hello-card" {
		t.Fatalf("id=%s", man.ID)
	}
	// greet
	out, err := m.Call("hello-card", "greet", map[string]any{"name": "测试"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["msg"] != "你好，测试！这是来自 goja 沙箱的问候。" {
		t.Fatalf("greet=%v", res)
	}
	// visit：存储持久化
	out, _ = m.Call("hello-card", "visit", nil)
	if out.(map[string]any)["visits"].(float64) != 1 {
		t.Fatalf("visit1=%v", out)
	}
	out, _ = m.Call("hello-card", "visit", nil)
	if out.(map[string]any)["visits"].(float64) != 2 {
		t.Fatalf("visit2=%v（存储未持久化？）", out)
	}
	// stats：DataFn 注入
	m.DataFn = func() any {
		return map[string]any{"free": []map[string]any{{"id": "a"}, {"id": "b"}}, "zcode": []map[string]any{{"id": "c"}}}
	}
	out, _ = m.Call("hello-card", "stats", nil)
	res = out.(map[string]any)
	if res["providers"] != float64(2) || res["models"] != float64(3) {
		t.Fatalf("stats=%v", res)
	}
	// 未知方法
	if _, err := m.Call("hello-card", "nope", nil); err == nil {
		t.Fatal("未知方法应报错")
	}
	// 停用后调用应失败
	if err := m.Toggle("hello-card", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("hello-card", "greet", nil); err == nil {
		t.Fatal("停用后调用应失败")
	}
	// 重启 manager：注册表保留；enabled=false 不自动启动
	dir := filepath.Dir(m.regPath())
	m2, err := NewManager(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	m2.Close()
	if _, err := m2.Call("hello-card", "greet", nil); err == nil {
		t.Fatal("停用状态下重启不应可调用")
	}
	// 重新启用后重启：startAll 路径应自动拉起运行时
	if err := m.Toggle("hello-card", true); err != nil {
		t.Fatal(err)
	}
	m3, err := NewManager(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer m3.Close()
	out3, err := m3.Call("hello-card", "greet", map[string]any{"name": "重启"})
	if err != nil {
		t.Fatalf("重启后调用失败: %v", err)
	}
	if out3.(map[string]any)["msg"] != "你好，重启！这是来自 goja 沙箱的问候。" {
		t.Fatalf("greet=%v", out3)
	}
	// 卸载
	if err := m.Uninstall("hello-card"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.dir("hello-card")); !os.IsNotExist(err) {
		t.Fatal("卸载后目录应删除")
	}
}

func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("缺 plugin.json 应报错")
	}
	_ = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"BAD-ID","name":"x","version":"1.0.0"}`), 0o600)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("非法 id 应报错")
	}
	_ = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"ok-id","name":"x","version":"1.0","main":"../evil.js"}`), 0o600)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("穿越 main 应报错")
	}
}
