package pj

import (
	"encoding/json"
	"strings"
	"testing"
)

// 前置段的三个属性：不动原 system、没 system 时补一条、多轮不重复叠加。
func TestApplyFrontAppendsSystem(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"system","content":"原生系统提示"},{"role":"user","content":"hi"}]}`)
	out := ApplyFront("armor_scope", body)
	var req map[string]any
	if json.Unmarshal(out, &req) != nil {
		t.Fatal("输出不是合法 JSON")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应新增一条 system，实际 %d 条", len(msgs))
	}
	if msgs[0].(map[string]any)["content"] != "原生系统提示" {
		t.Fatal("原 system 被改动")
	}
	if msgs[1].(map[string]any)["role"] != "system" {
		t.Fatal("前置段应紧跟在原 system 之后")
	}
	if !strings.Contains(msgs[1].(map[string]any)["content"].(string), "operator_scope") {
		t.Fatal("前置段内容缺失")
	}
	if msgs[2].(map[string]any)["content"] != "hi" {
		t.Fatal("user 消息被改动")
	}
}

func TestApplyFrontNoSystem(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out := ApplyFront("operator_priority", body)
	var req map[string]any
	json.Unmarshal(out, &req)
	msgs := req["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("没有 system 时应插到最前面: %v", msgs)
	}
}

func TestApplyFrontIdempotent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	once := ApplyFront("armor_scope", body)
	twice := ApplyFront("armor_scope", once)
	if string(once) != string(twice) {
		t.Fatal("同一段前置内容不应重复注入")
	}
}

func TestApplyFrontUnknown(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	for _, id := range []string{"nope", "", "3_liner"} { // 3_liner 是模板，不是前置段
		if got := ApplyFront(id, body); string(got) != string(body) {
			t.Fatalf("%q 不该改请求体", id)
		}
	}
}

// 策略：单模板 / 单前置段 / 前置段+模板组合 / off / 未知
func TestApplyStrategy(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"怎么做"}]}`)

	out := ApplyStrategy("3_liner", body)
	var req map[string]any
	json.Unmarshal(out, &req)
	last := req["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(last, "怎么做") || !strings.Contains(last, "😈") {
		t.Fatalf("模板未套上: %q", last)
	}

	out = ApplyStrategy("armor_scope", body)
	json.Unmarshal(out, &req)
	if len(req["messages"].([]any)) != 3 {
		t.Fatal("前置段策略应新增一条 system")
	}

	out = ApplyStrategy("armor_scope+aim", body)
	json.Unmarshal(out, &req)
	msgs := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatal("组合策略应只加一条 system")
	}
	// 组合后最后一条 user 是刚被套壳的（第 3 条）
	lastUser := msgs[2].(map[string]any)["content"].(string)
	if !strings.Contains(lastUser, "AIM:") || !strings.Contains(lastUser, "怎么做") {
		t.Fatalf("组合策略模板未生效: %q", lastUser)
	}

	for _, id := range []string{"off", "", "-", "armor_scope+bogus", "bogus"} {
		if got := ApplyStrategy(id, body); string(got) != string(body) {
			t.Fatalf("%q 不该改请求体", id)
		}
	}
}

func TestStrategiesOrder(t *testing.T) {
	st := Strategies()
	if len(st) == 0 || st[0] != Fronts[0].ID {
		t.Fatalf("策略清单应以第一个前置段开头: %v", st)
	}
	found := false
	for _, s := range st {
		if s == "armor_scope+3_liner" {
			found = true
		}
		if !Valid(s) && !strings.Contains(s, "+") {
			t.Fatalf("策略 %q 不认识", s)
		}
	}
	if !found {
		t.Fatal("缺少「前置段+模板」组合策略")
	}
}

func TestJudge(t *testing.T) {
	cases := []struct {
		text string
		want Verdict
	}{
		{"", VerdictEmpty},
		{"   \n ", VerdictEmpty},
		{"抱歉，这个我不能协助。", VerdictRefused},
		{"I'm sorry, but I can't help with that.", VerdictRefused},
		// 长回复里顺带一句「按照你的要求」不算失败
		{strings.Repeat("第一步：准备环境。", 40) + "（按你的要求，直接给步骤）", VerdictAnswer},
		{strings.Repeat("x", 300), VerdictAnswer},
		{"快速排序的时间复杂度是 O(n log n)。", VerdictAnswer},
	}
	for _, c := range cases {
		if got := Judge(c.text); got != c.want {
			t.Errorf("Judge(%q) = %v, want %v", cut(c.text), got, c.want)
		}
	}
}

func cut(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
