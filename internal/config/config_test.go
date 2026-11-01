package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 手改过的 config.json 带 BOM 时也要能起来（Windows 记事本 / PS5 Set-Content 常带）。
func TestLoadToleratesBOM(t *testing.T) {
	dir := t.TempDir()
	body := append([]byte{0xEF, 0xBB, 0xBF},
		[]byte(`{"apiKey":"sk-th-bomtest","pjEnabled":false,"pjMode":"aim"}`)...)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("带 BOM 的配置应能加载: %v", err)
	}
	if cfg.APIKey != "sk-th-bomtest" {
		t.Fatalf("apiKey = %q", cfg.APIKey)
	}
	if cfg.PJEnabled || cfg.PJMode != "aim" {
		t.Fatalf("破甲字段没读对: enabled=%v mode=%q", cfg.PJEnabled, cfg.PJMode)
	}
}

// 旧配置（没有 pjEnabled / pjScope，pjMode=off）迁移后：开关默认开、范围默认 all、策略保留 off。
func TestLoadMigratesLegacyPJ(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"apiKey":"sk-th-old","pjMode":"off"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PJEnabled {
		t.Fatal("缺失 pjEnabled 时应取默认 true")
	}
	if cfg.PJScope != PJScopeAll {
		t.Fatalf("缺失 pjScope 时应为 all，实际 %q", cfg.PJScope)
	}
	if cfg.PJMode != "off" || cfg.PJOn() {
		t.Fatalf("pjMode=off 应原样保留且不生效: %q active=%v", cfg.PJMode, cfg.PJOn())
	}
}

func TestPJOn(t *testing.T) {
	cases := []struct {
		enabled bool
		mode    string
		want    bool
	}{
		{true, "armor_scope", true},
		{true, "operator_priority", true},
		{true, "armor_scope+3_liner", true},
		{true, "auto", true},
		{true, "off", false},
		{true, "", false},
		{true, "bogus", false},
		{true, "armor_scope+bogus", false},
		{false, "armor_scope", false},
	}
	for _, c := range cases {
		cfg := Default()
		cfg.PJEnabled = c.enabled
		cfg.PJMode = c.mode
		if got := cfg.PJOn(); got != c.want {
			t.Errorf("PJOn(enabled=%v, mode=%q) = %v, want %v", c.enabled, c.mode, got, c.want)
		}
	}
}

// 未知策略会被归一化回默认前置段，不会让破甲悄悄失效。
func TestNormalizeUnknownMode(t *testing.T) {
	cfg := Default()
	cfg.PJMode = "totally-unknown"
	cfg.Normalize()
	if cfg.PJMode != "armor_scope" {
		t.Fatalf("未知策略应回落到 armor_scope，实际 %q", cfg.PJMode)
	}
}
