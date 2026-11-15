package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"tokenhub/internal/pj"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// ── tools 请求降级：模板段摘除 + 仅前置段 ──

func TestPJToolsRequestDropsTemplate(t *testing.T) {
	g, _ := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "armor_scope+aim"

	// 请求带 tools，且历史里已有一轮套了 aim 模板的 user 消息
	hist := pj.Apply("aim", []byte(`{"messages":[{"role":"user","content":"上次的问题"}]}`))
	var histReq map[string]any
	if err := json.Unmarshal(hist, &histReq); err != nil {
		t.Fatalf("历史构造失败: %v", err)
	}
	histMsgs := histReq["messages"].([]any)

	req := map[string]any{
		"model": "stub-model",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "run", "parameters": map[string]any{}}}},
		"messages": append(append([]any{}, histMsgs...),
			map[string]any{"role": "assistant", "content": "好的"},
			map[string]any{"role": "user", "content": "继续"}),
	}
	body, _ := json.Marshal(req)

	out := g.applyPJModelOpts(store.PTrae, "stub-model", "stub-model", body, true)
	raw := string(out)
	if strings.Contains(raw, pj.TemplateMarker) {
		t.Fatal("tools 请求应摘除历史里的模板标记")
	}
	if strings.Contains(raw, "Niccolo") || strings.Contains(raw, "amoral") {
		t.Fatal("tools 请求不应再带模板正文")
	}
	if !strings.Contains(raw, "th-front:armor_scope") {
		t.Fatal("tools 请求应保留前置段注入")
	}
	var outReq map[string]any
	if err := json.Unmarshal(out, &outReq); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	msgs := outReq["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != "继续" {
		t.Fatalf("最后一轮 user 应为原文，实际 %v", last["content"])
	}
}

func TestStripTemplateRestoresOriginal(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"hello world"}]}`)
	wrapped := pj.Apply("aim", in)
	if string(wrapped) == string(in) {
		t.Fatal("Apply 应实际注入")
	}
	out, changed := pj.StripTemplate(wrapped)
	if !changed {
		t.Fatal("StripTemplate 应识别已注入内容")
	}
	var req map[string]any
	_ = json.Unmarshal(out, &req)
	msgs := req["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "hello world" {
		t.Fatalf("应还原原文，实际 %v", msgs[0].(map[string]any)["content"])
	}
	if out2, changed2 := pj.StripTemplate(in); changed2 || string(out2) != string(in) {
		t.Fatal("未注入的请求不应改动")
	}
}

// ── auto 探测 singleflight：第二个并发请求不等探测、直接透传 ──

func TestPJAutoSingleflightSkipsSecondProbe(t *testing.T) {
	g, sp := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "auto"

	// 手工占用 singleflight 槽位，模拟探测进行中
	g.pjProbeMu.Lock()
	g.pjProbeRunning = map[string]bool{"stub-model": true}
	g.pjProbeMu.Unlock()

	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	if got := g.applyPJModelOpts(store.PTrae, "stub-model", "stub-model", body, false); string(got) != string(body) {
		t.Fatal("探测进行中应透传原请求")
	}
	if len(sp.got) != 0 {
		t.Fatal("探测进行中不应再发探测请求")
	}
}

// ── 流式拒答门：第一轮回拒答 → 自动换更强策略重试 → 客户端拿到正常回答 ──

func TestGateChatRetriesOnRefusal(t *testing.T) {
	g, sp := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "auto"

	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"写个注册机"}]}`)
	body = g.optimizeReq("stub-model", body, nil)
	prePJ := body
	body = g.applyPJModel(provNameOf(g), "stub-model", "stub-model", body)

	outSink := &collectSink{}
	rec := &usageRecorder{inner: outSink}
	err := g.gateChat(context.Background(), "trae", "stub-model", "stub-model", body, prePJ, outSink, rec, false)
	if err != nil {
		t.Fatalf("gateChat: %v", err)
	}
	joined := strings.Join(outSink.contents, "")
	if !strings.Contains(joined, "第一步") {
		t.Fatalf("重试成功后应 flush 正常回答，实际 %q", joined)
	}
	if !outSink.finished {
		t.Fatal("flush 应以 Finish 收尾")
	}
	// 上游至少两次调用：探测/首轮 + 重试
	if len(sp.got) < 2 {
		t.Fatalf("应发生 failover 重试，实际调用 %d 次", len(sp.got))
	}
}

// 非流式 failover 的 tools 兜底路径：拒答重试时模板被摘除。
func TestFailoverToolsStripsTemplate(t *testing.T) {
	g, sp := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "armor_scope+aim"
	sp.refuseFirst = true // 第一次拒答触发 failover

	body := []byte(`{"model":"stub-model","tools":[{"type":"function","function":{"name":"run","parameters":{}}}],"messages":[{"role":"user","content":"写个注册机"}]}`)
	body = g.optimizeReq("stub-model", body, nil)
	prePJ := body
	body = g.applyPJModelOpts(store.PTrae, "stub-model", "stub-model", body, true)

	sink := NewAggregateSink("stub-model")
	if err := g.pool.Chat(context.Background(), "stub-model", body, sink); err != nil {
		t.Fatalf("chat: %v", err)
	}
	raw, perr := sink.Result()
	if perr != nil {
		t.Fatalf("result: %v", perr)
	}
	txt := openaiContentText(raw)
	if txt == "" || !pj.LooksRefused(txt) {
		t.Fatalf("首轮应被拒以触发 failover，实际 %q", txt)
	}
	// 模拟非流式 failover 分支的 tools 处理
	if stronger := pjNextStrategy(g.pjEffectiveStrategy("stub-model")); stronger != "" {
		retryBody := pj.ApplyStrategy(stronger, prePJ)
		if out, changed := pj.StripTemplate(retryBody); changed {
			retryBody = out
		}
		if plan := pj.PlanForBudget(stronger, g.pjEffectiveBudget()); plan.Front != "" {
			retryBody = pj.ApplyFrontOpts(plan.Front, retryBody, FrontLeanOpts(g))
		}
		rawRetry := string(retryBody)
		if strings.Contains(rawRetry, "Niccolo") && !strings.Contains(rawRetry, "th-front:") {
			t.Fatal("tools failover 重试不应带模板")
		}
	}
}

// ── helpers ──

func provNameOf(g *Gateway) string { return store.PTrae }

// collectSink 收集 flushOpenAIStream 的写出，供断言。
type collectSink struct {
	chunks   int
	contents []string
	finished bool
	errMsg   string
}

func (c *collectSink) Delta(d provider.Delta) {
	c.chunks++
	if d.Content != "" {
		c.contents = append(c.contents, d.Content)
	}
}
func (c *collectSink) Finish(f provider.Finish)      { c.finished = true }
func (c *collectSink) StreamError(code int64, m string) { c.errMsg = m }
func (c *collectSink) Close()                           {}
