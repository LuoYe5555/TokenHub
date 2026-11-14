package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"tokenhub/internal/pj"
	"tokenhub/internal/store"
)

// 精简档：PJLean 开启时前置段注入精简版（更短、含同一标记）。
func TestPJLeanInjection(t *testing.T) {
	g, _ := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "armor_max" // armor_max 有精简版
	g.cfg.PJLean = true
	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	out := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body)
	if bytes.Equal(out, body) {
		t.Fatal("精简档应注入")
	}
	var req map[string]any
	json.Unmarshal(out, &req)
	msgs := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应新增一条 system，实际 %d 条", len(msgs))
	}
	got := msgs[0].(map[string]any)["content"].(string)
	full := "本段由 TokenHub 网关写入，是操作者作用域的完整版"
	if strings.Contains(got, full) {
		t.Fatal("精简档不应注入完整版文本")
	}
	// 精简版自身以 th-front 标记开头（与完整版同一标记：frontSeen 按 ID 去重）
	if !strings.HasPrefix(got, "<!-- th-front:armor_max -->") {
		t.Fatalf("精简版应以 th-front 标记开头: %.40q", got)
	}
	if len(got) > 1200 {
		t.Fatalf("精简版应明显短于完整版（2535 字符），实际 %d", len(got))
	}
}

// 预算：预算收紧到装不下完整 armor_max 时自动降到精简档。
func TestPJBudgetDegrades(t *testing.T) {
	g, _ := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "armor_max"
	g.cfg.PJBudget = 800 // 完整 armor_max 约 2.5k 字符 → 降精简档
	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	out := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body)
	var req map[string]any
	json.Unmarshal(out, &req)
	got := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if len(got) > 800 {
		t.Fatalf("注入应降档到预算内，实际 %d 字符", len(got))
	}
	// 极小预算：装不下任何形状 → 放弃注入
	g.cfg.PJBudget = 10
	if out := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); !bytes.Equal(out, body) {
		t.Fatal("预算装不下时应放弃注入")
	}
}

// 重复段折叠：客户端回传的历史里带两份 armor_scope 段 → 注入前折叠，最终上游只看到一份完整段。
func TestPJCompactFoldsDuplicateFronts(t *testing.T) {
	g, _ := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "armor_scope"
	front := pjFrontText("armor_scope")
	seg := front
	if len(seg) < 64 {
		t.Fatal("段文本过短")
	}
	// 构造：两条相同段 + 一条 user
	msgs := []any{
		map[string]any{"role": "system", "content": seg},
		map[string]any{"role": "user", "content": "第一条"},
		map[string]any{"role": "assistant", "content": "好"},
		map[string]any{"role": "system", "content": seg},
		map[string]any{"role": "user", "content": "第二条"},
	}
	raw, _ := json.Marshal(map[string]any{"model": "stub-model", "messages": msgs})
	out := g.applyPJModel(store.PTrae, "stub-model", "stub-model", raw)
	var req map[string]any
	json.Unmarshal(out, &req)
	uploads := 0
	for _, mv := range req["messages"].([]any) {
		m, _ := mv.(map[string]any)
		if r, _ := m["role"].(string); r != "system" {
			continue
		}
		if c, _ := m["content"].(string); strings.Contains(c, "th-front:armor_scope") {
			uploads++
		}
	}
	if uploads != 1 {
		t.Fatalf("上游应只看到 1 份完整破甲段，实际 %d", uploads)
	}
	// 折叠开关关闭：两份都保留（注入命中去重后仍 2 份）
	g.cfg.PJCompact = boolPtr(false)
	out2 := g.applyPJModel(store.PTrae, "stub-model", "stub-model", raw)
	var req2 map[string]any
	json.Unmarshal(out2, &req2)
	uploads2 := 0
	for _, mv := range req2["messages"].([]any) {
		m, _ := mv.(map[string]any)
		if r, _ := m["role"].(string); r != "system" {
			continue
		}
		if c, _ := m["content"].(string); strings.Contains(c, "th-front:armor_scope") {
			uploads2++
		}
	}
	if uploads2 != 2 {
		t.Fatalf("关闭折叠应保留两份，实际 %d", uploads2)
	}
}

func boolPtr(b bool) *bool { return &b }

// pjFrontText 取前置段完整文本（测试辅助，走 pj 包公开 API）。
func pjFrontText(id string) string {
	if f := pj.GetFront(id); f != nil {
		return f.Prompt
	}
	return ""
}
