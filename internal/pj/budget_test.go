package pj

import (
	"encoding/json"
	"strings"
	"testing"
)

// 新段 armor_crack：是前置段、可取到、被 Valid 认可。
func TestArmorCrackRegistered(t *testing.T) {
	f := GetFront("armor_crack")
	if f == nil {
		t.Fatal("armor_crack 前置段未注册")
	}
	if !Valid("armor_crack") || !IsFront("armor_crack") {
		t.Fatal("armor_crack 分类异常")
	}
	for _, kw := range []string{"keygen", "补丁", "DRM", "资源检索", "operator_scope"} {
		if !strings.Contains(f.Prompt, kw) {
			t.Fatalf("armor_crack 缺少关键词 %q", kw)
		}
	}
	if f.Lean == "" || len(f.Lean) >= len(f.Prompt) {
		t.Fatal("armor_crack 应有更短的精简版")
	}
	if !strings.Contains(f.Lean, frontMark("armor_crack")) {
		t.Fatal("精简版缺少 th-front 标记")
	}
}

// evil_confident 模板：注册 + 占位符替换。
func TestEvilConfidentTemplate(t *testing.T) {
	tpl := Get("evil_confident")
	if tpl == nil {
		t.Fatal("evil_confident 模板未注册")
	}
	if !strings.Contains(tpl.Prompt, insertMark) {
		t.Fatal("模板应含占位符")
	}
	body := []byte(`{"messages":[{"role":"user","content":"问题"}]}`)
	out := Apply("evil_confident", body)
	var req map[string]any
	json.Unmarshal(out, &req)
	c := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(c, "[INSERT PROMPT HERE]") || !strings.Contains(c, "Evil Confident") {
		t.Fatalf("套壳异常: %q", c)
	}
}

// 新段进 auto 策略清单，且排序符合强度规则：
// armor_max > armor_crack > armor_re > armor_scope > operator_priority。
func TestNewFrontStrategyOrder(t *testing.T) {
	st := Strategies()
	rank := func(id string) int {
		for i, s := range st {
			if s == id {
				return i
			}
		}
		return -1
	}
	rMax, rCrack, rRe, rScope, rOp := rank("armor_max"), rank("armor_crack"), rank("armor_re"), rank("armor_scope"), rank("operator_priority")
	if !(rMax < rCrack && rCrack < rRe && rRe < rScope && rScope < rOp) {
		t.Fatalf("前置段强度排序异常: max=%d crack=%d re=%d scope=%d op=%d", rMax, rCrack, rRe, rScope, rOp)
	}
	// 同段：带模板组合排在裸段之前（模板是加强件），且仍弱于更强的裸段
	if rank("armor_max+aim") >= rMax || rank("armor_max+aim") >= rCrack {
		t.Fatalf("armor_max+aim 应在 armor_max 与 armor_crack 之间: max=%d combo=%d crack=%d", rMax, rank("armor_max+aim"), rCrack)
	}
	// 带模板组合排在所有裸模板之前
	if rank("armor_scope+3_liner") > rank("aim") {
		t.Fatalf("组合应排在裸模板前: combo=%d aim=%d", rank("armor_scope+3_liner"), rank("aim"))
	}
	for _, s := range st {
		if s == "evil_confident" {
			return
		}
	}
	t.Fatal("auto 策略清单缺少 evil_confident")
}

// 预算计划：预算内走完整版；收紧预算降到精简；再收紧砍模板；极小预算放弃。
func TestPlanForBudget(t *testing.T) {
	full := len(GetFront("armor_max").Prompt) + len(Get("aim").Prompt)
	leanMax := len(GetFront("armor_max").Lean)

	// 预算充足：完整版，含模板
	p := PlanForBudget("armor_max+aim", full+100)
	if !p.Has || p.Lean || p.Front != "armor_max" {
		t.Fatalf("充足预算应完整注入: %+v", p)
	}

	// 预算装不下完整版但装得下精简段+模板：降精简
	budget := full - 1
	if leanMax+len(Get("aim").Prompt) <= budget {
		p = PlanForBudget("armor_max+aim", budget)
		if !p.Has || !p.Lean || p.Front != "armor_max" {
			t.Fatalf("应降精简档: %+v", p)
		}
	}

	// 只装得下精简段：砍模板
	p = PlanForBudget("armor_max+aim", leanMax-1)
	if p.Has {
		t.Fatalf("精简段都放不下时最终应放弃: %+v", p)
	}

	// 不限预算
	p = PlanForBudget("armor_scope", 0)
	if !p.Has || p.Lean {
		t.Fatalf("不限预算应完整注入: %+v", p)
	}

	// off / 未知
	if p := PlanForBudget("off", 0); p.Has {
		t.Fatal("off 不应产生注入计划")
	}
}

// ApplyPlan：按计划落注入，放弃计划原样返回。
func TestApplyPlan(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	plan := PlanForBudget("armor_max+aim", 0)
	out := ApplyPlan(plan, body)
	if string(out) == string(body) {
		t.Fatal("计划内应注入")
	}
	var req map[string]any
	json.Unmarshal(out, &req)
	msgs := req["messages"].([]any)
	if len(msgs) != 3 || !strings.Contains(msgs[1].(map[string]any)["content"].(string), "th-front:armor_max") {
		t.Fatal("armor_max 段未注入")
	}
	if got := msgs[2].(map[string]any)["content"].(string); !strings.Contains(got, "AIM") || !strings.Contains(got, "hi") {
		t.Fatalf("模板未套上: %q", got)
	}
	// 放弃计划
	empty := PlanForBudget("armor_max+aim", 10)
	if again := ApplyPlan(empty, body); string(again) != string(body) {
		t.Fatal("放弃计划时不应改动请求")
	}
}

// 精简档注入：Lean=true 时写入的是 Lean 文本；完整↔精简互换时原地升级不重复叠加。
func TestLeanFrontInjection(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	full := ApplyFrontOpts("armor_max", body, FrontOpts{Lean: false})
	lean := ApplyFrontOpts("armor_max", full, FrontOpts{Lean: true})
	if string(full) == string(lean) {
		t.Fatal("完整→精简应原地替换")
	}
	var req map[string]any
	json.Unmarshal(lean, &req)
	msgs := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("精简替换不应新增消息，实际 %d 条", len(msgs))
	}
	if got := msgs[1].(map[string]any)["content"].(string); !strings.Contains(got, frontMark("armor_max")) || len(got) >= len(GetFront("armor_max").Prompt) {
		t.Fatal("注入的应为精简版内容")
	}
	// 再切回完整：同样原地替换，不叠加
	back := ApplyFrontOpts("armor_max", lean, FrontOpts{Lean: false})
	var req2 map[string]any
	json.Unmarshal(back, &req2)
	if len(req2["messages"].([]any)) != 3 {
		t.Fatal("精简→完整不应新增消息")
	}
	// 同形状幂等
	if again := ApplyFrontOpts("armor_max", back, FrontOpts{Lean: false}); string(again) != string(back) {
		t.Fatal("同形状重复注入应幂等")
	}
}

// 旧标记（armor_scope 老版本无 th-front 标记）不会误判已注入——按新标记重新注入。
func TestFrontSeenByMark(t *testing.T) {
	old := []byte(`{"messages":[{"role":"system","content":"<!-- armor-scope -->\n<operator_scope>旧版段落</operator_scope>"},{"role":"user","content":"hi"}]}`)
	out := ApplyFront("armor_scope", old)
	var req map[string]any
	json.Unmarshal(out, &req)
	// 旧段无标记 → 应新增一条（原旧 system 保留）
	if len(req["messages"].([]any)) != 3 {
		t.Fatalf("旧版段应视为未注入，实际消息数 %d", len(req["messages"].([]any)))
	}
	// 数组形态 content：标记命中后原地升级为完整段文本（不新增消息、保留数组形态）
	arrStr := `{"messages":[{"role":"system","content":[{"type":"text","text":"prefix ` + frontMark("armor_scope") + ` body"}]},{"role":"user","content":"hi"}]}`
	arr := []byte(arrStr)
	out3 := ApplyFront("armor_scope", arr)
	var req3 map[string]any
	if json.Unmarshal(out3, &req3) != nil {
		t.Fatal("输出不是合法 JSON")
	}
	msgs3 := req3["messages"].([]any)
	if len(msgs3) != 2 {
		t.Fatalf("数组命中应原地升级，不新增消息，实际 %d 条", len(msgs3))
	}
	blk3 := msgs3[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if !strings.Contains(blk3["text"].(string), frontMark("armor_scope")) || !strings.Contains(blk3["text"].(string), "operator_scope") {
		t.Fatal("数组 content 未升级为新版段落")
	}
	if msgs3[1].(map[string]any)["content"] != "hi" {
		t.Fatal("user 消息被改动")
	}
	// 数组形态旧版内容（无本段标记）→ 视为未注入：新插一条 system，原消息保留
	oldArr := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"旧版 armor_scope 段落"}]},{"role":"user","content":"hi"}]}`)
	upgraded := ApplyFront("armor_scope", oldArr)
	var req2 map[string]any
	json.Unmarshal(upgraded, &req2)
	msgs2 := req2["messages"].([]any)
	if len(msgs2) != 3 {
		t.Fatalf("无标记的旧数组段应新插一条 system，实际 %d 条", len(msgs2))
	}
	if !strings.Contains(msgs2[1].(map[string]any)["content"].(string), frontMark("armor_scope")) {
		t.Fatal("新插的 system 缺少新版段落")
	}
}
