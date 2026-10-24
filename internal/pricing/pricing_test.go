package pricing

import (
	"context"
	"os"
	"testing"
)

func getenv(k string) string { return os.Getenv(k) }
func testCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 120*1e9)
	_ = cancel
	return ctx
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"zcode:glm-5.3-flash":         "glm-5-3-flash",
		"qw:qwen3.8-max":              "qwen3-8-max",
		"ol:qwen2.5:7b":               "qwen2-5-7b",
		"z-ai/glm-5.3":                "z-ai-glm-5-3",
		"GLM-4.6":                     "glm-4-6",
		"anthropic/claude-sonnet-4.5": "anthropic-claude-sonnet-4-5",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCostBuiltin(t *testing.T) {
	tbl := Load(t.TempDir())
	// GLM-5.3-Flash 内置价：输入 0.8 / 输出 2.8 / 缓存读 0.23（CNY 每百万）
	cost, src, ok := tbl.Cost("zcode:glm-5.3-flash", 1_000_000, 1_000_000, 0, 0)
	if !ok {
		t.Fatal("glm-5.3-flash 未匹配到价格")
	}
	t.Logf("glm-5.3-flash: src=%s cost=%.2f CNY", src, cost)
	if src != "builtin" {
		t.Errorf("离线应命中 builtin，实际 %s", src)
	}
	if cost < 3.4 || cost > 3.8 {
		t.Errorf("花费 %.2f 不在预期 3.6 附近", cost)
	}
	// 缓存读：100 万输入 + 100 万缓存读 ≈ 0.8 + 0.23
	cost2, _, _ := tbl.Cost("zcode:glm-5.3-flash", 0, 0, 1_000_000, 0)
	if cost2 < 0.2 || cost2 > 0.26 {
		t.Errorf("缓存读花费 %.3f 不在预期 0.23 附近", cost2)
	}
	// USD 模型按汇率折 CNY：造一条假价
	tbl.mu.Lock()
	tbl.prices["test-usd-model"] = &ModelPrice{ID: "test/usd-model", Input: 1, Output: 2, Currency: "USD", Source: "openrouter"}
	tbl.rate = 7.0
	tbl.mu.Unlock()
	cost3, _, _ := tbl.Cost("test-usd-model", 1_000_000, 0, 0, 0)
	if cost3 != 7.0 {
		t.Errorf("USD 折算 = %.2f, want 7.0", cost3)
	}
	// 未匹配 → ok=false
	if _, _, ok := tbl.Cost("totally-unknown-model-xyz", 1000, 1000, 0, 0); ok {
		t.Error("未知模型不应计价")
	}
}

func TestRefreshLive(t *testing.T) {
	if getenv("PRICE_LIVE") == "" {
		t.Skip("需要 PRICE_LIVE=1")
	}
	tbl := Load(t.TempDir())
	if err := tbl.Refresh(testCtx()); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	up, rate, n := tbl.Meta()
	t.Logf("刷新完成: %d 模型, rate=%.4f, updatedAt=%d", n, rate, up)
	cost, src, ok := tbl.Cost("zcode:glm-5.3-flash", 1_000_000, 1_000_000, 0, 0)
	t.Logf("glm-5.3-flash: ok=%v src=%s cost=%.2f CNY", ok, src, cost)
	for _, m := range []string{"zcode:glm-5.3", "qw:qwen3.8-max", "ol:qwen2.5:7b"} {
		c, s, o := tbl.Cost(m, 1_000_000, 1_000_000, 0, 0)
		t.Logf("%s: ok=%v src=%s cost=%.2f CNY/百万组合", m, o, s, c)
	}
}
