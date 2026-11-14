package pj

// 破甲注入预算：前置段+模板叠加起来可能很肥（armor_max 完整版 ~1500 tokens，
// 再套 aim 模板又是 ~400），长上下文请求里占比过高。这里按字符预算逐级降档：
//
//	预算内                    → 按操作者选的策略原样注入（完整版）
//	超预算、段有精简版        → 前置段换精简版（token 成本约 1/3）
//	仍超预算                  → 只留最强前置段（砍模板），保住「作用域」这层底线
//	再超（理论上不会）        → 完全放弃注入，宁可不打也不打半截
//
// 判定都在纯文本层完成（EstTokens 口径折算字符），不解析 JSON——
// 网关在 ApplyStrategy 之前先问这里要「该用什么形状注入」。

import "strings"

// BudgetPlan 预算裁剪后的注入计划。
type BudgetPlan struct {
	Lean  bool   // 前置段用精简版
	Front string // 实际要挂的前置段 ID（"" = 只挂模板或不挂）
	Tpl   string // 实际要套的模板 ID（"" = 不套）
	Has   bool   // 是否有可注入的内容（false = 全部放弃）
}

// ApplyPlan 把预算计划实际套到请求体上：前置段（按 plan 形状）+ 模板。
// plan.Has=false 时原样返回。
func ApplyPlan(plan BudgetPlan, body []byte) []byte {
	if !plan.Has {
		return body
	}
	out := body
	if plan.Front != "" {
		out = ApplyFrontOpts(plan.Front, out, FrontOpts{Lean: plan.Lean})
	}
	if plan.Tpl != "" {
		out = Apply(plan.Tpl, out)
	}
	return out
}

// PlanForBudget 按预算给策略选注入形状。strategy 为完整策略 ID（可含 +）。
// budget<=0 表示不限。返回的 plan 传给 gateway 的注入管线；
// plan.Has=false 表示预算装不下任何形状（网关应放弃注入）。
func PlanForBudget(strategy string, budget int) BudgetPlan {
	plan := BudgetPlan{Lean: false, Front: "", Has: false}
	if strategy == "" || strategy == "off" || strategy == "-" {
		return plan
	}
	front, tpl := "", ""
	for _, p := range strings.Split(strategy, "+") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if GetFront(p) != nil {
			if front == "" || FrontRank[p] > FrontRank[front] {
				front = p
			}
			continue
		}
		if Get(p) != nil {
			tpl = p
		}
	}
	if front == "" && tpl == "" {
		return plan
	}
	cost := func(lean bool) int {
		n := 0
		if f := GetFront(front); f != nil {
			s := f.Prompt
			if lean && f.Lean != "" {
				s = f.Lean
			}
			n += len(s)
		}
		if t := Get(tpl); t != nil {
			n += len(t.Prompt)
		}
		return n
	}
	if budget <= 0 || cost(false) <= budget {
		plan.Front = front
		plan.Tpl = tpl
		plan.Has = front != "" || tpl != ""
		return plan
	}
	// 第一档降级：前置段换精简版
	if cost(true) <= budget {
		plan.Lean = true
		plan.Front = front
		plan.Tpl = tpl
		plan.Has = true
		return plan
	}
	// 第二档：砍掉模板，只留（精简）前置段
	if f := GetFront(front); f != nil {
		s := f.Lean
		if s == "" {
			s = f.Prompt
		}
		if budget <= 0 || len(s) <= budget {
			plan.Lean = true
			plan.Front = front
			plan.Tpl = ""
			plan.Has = true
			return plan
		}
	}
	// 兜底：预算小到连精简段都放不下 → 完全放弃（宁可不打也不打半截）
	return BudgetPlan{Lean: true, Front: "", Tpl: "", Has: false}
}
