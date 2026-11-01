package custom

import "testing"

// 地址带版本段结尾时直接追加动作；裸域名补 /v1。
// 回归背景：用户填 https://open.bigmodel.cn/api/paas/v4，旧逻辑只认 /v1，
// 拼出 /v4/v1/chat/completions 必然 404。
func TestEndpoints(t *testing.T) {
	cases := []struct {
		base, style, chat, models string
	}{
		// 裸域名 → 补 /v1（https:// 前缀由 BaseURL 归一化时补，这里只管拼接）
		{"https://api.deepseek.com", "openai", "https://api.deepseek.com/v1/chat/completions", "https://api.deepseek.com/v1/models"},
		// 显式 /v1 → 直接追加
		{"https://api.deepseek.com/v1", "openai", "https://api.deepseek.com/v1/chat/completions", "https://api.deepseek.com/v1/models"},
		{"https://api.deepseek.com/v1/", "openai", "https://api.deepseek.com/v1/chat/completions", "https://api.deepseek.com/v1/models"},
		// 其他版本段结尾 → 直接追加，不插 /v1
		{"https://open.bigmodel.cn/api/paas/v4", "openai", "https://open.bigmodel.cn/api/paas/v4/chat/completions", "https://open.bigmodel.cn/api/paas/v4/models"},
		{"https://open.bigmodel.cn/v4", "openai", "https://open.bigmodel.cn/v4/chat/completions", "https://open.bigmodel.cn/v4/models"},
		{"https://example.com/v2", "openai", "https://example.com/v2/chat/completions", "https://example.com/v2/models"},
		{"https://generativelanguage.googleapis.com/v1beta", "openai", "https://generativelanguage.googleapis.com/v1beta/chat/completions", "https://generativelanguage.googleapis.com/v1beta/models"},
		// anthropic
		{"https://api.anthropic.com", "anthropic", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/models"},
		{"https://api.anthropic.com/v1", "anthropic", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/models"},
		// 不误伤：普通路径结尾仍补 /v1
		{"https://proxy.example.com/openai", "openai", "https://proxy.example.com/openai/v1/chat/completions", "https://proxy.example.com/openai/v1/models"},
		{"https://host/api/vip", "openai", "https://host/api/vip/v1/chat/completions", "https://host/api/vip/v1/models"},
	}
	for _, c := range cases {
		chat, models := endpoints(c.base, c.style)
		if chat != c.chat || models != c.models {
			t.Errorf("endpoints(%q, %q) = %q, %q; want %q, %q", c.base, c.style, chat, models, c.chat, c.models)
		}
	}
}

func TestHasVersionSegment(t *testing.T) {
	yes := []string{"https://x/v1", "https://x/api/paas/v4", "https://x/v1beta", "https://x/V3"}
	no := []string{"https://x", "https://x/openai", "https://x/api/vip", "https://x/v", "https://x/vintage"}
	for _, b := range yes {
		if !hasVersionSegment(b) {
			t.Errorf("hasVersionSegment(%q) 应为 true", b)
		}
	}
	for _, b := range no {
		if hasVersionSegment(b) {
			t.Errorf("hasVersionSegment(%q) 应为 false", b)
		}
	}
}
