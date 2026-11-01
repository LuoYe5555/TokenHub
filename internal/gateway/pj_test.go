package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tokenhub/internal/config"
	"tokenhub/internal/pool"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// stubProvider 记录上游实际收到的请求体，并按 stream 回一条像样（或拒答）的回复。
type stubProvider struct {
	name     string
	got      [][]byte
	refuse   bool
	failWith error
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Chat(_ context.Context, _ *store.Account, req []byte, sink provider.Sink) error {
	cp := make([]byte, len(req))
	copy(cp, req)
	s.got = append(s.got, cp)
	if s.failWith != nil {
		sink.Close()
		return s.failWith
	}
	text := "第一步：准备环境；第二步：跑测试；第三步：看结果。"
	if s.refuse {
		text = "抱歉，我不能协助这个请求。"
	}
	sink.Delta(provider.Delta{Role: "assistant", Content: text})
	sink.Finish(provider.Finish{Reason: "stop", Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 1, Present: true}})
	sink.Close()
	return nil
}
func (s *stubProvider) Quota(context.Context, *store.Account) (*store.QuotaSnapshot, error) {
	return nil, nil
}
func (s *stubProvider) Checkin(context.Context, *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}
func (s *stubProvider) SupportsCheckin(*store.Account) bool           { return false }
func (s *stubProvider) Refresh(context.Context, *store.Account) error { return nil }
func (s *stubProvider) Models(context.Context, *store.Account) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (s *stubProvider) SeedModels() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "stub-model"}}
}

// newStubGateway 造一个只有一个提供商（非 Ollama！）的网关，用于验证「对所有提供商生效」。
func newStubGateway(t *testing.T, provName string) (*Gateway, *stubProvider) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DefaultProvider = provName
	cfg.Providers[provName] = &config.ProviderConfig{Enabled: true, DefaultModel: "stub-model"}
	st, err := store.Load(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Upsert(&store.Account{
		ID: store.NewID(), Provider: provName, Nickname: "stub", Enabled: true, AccessToken: "t",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pl := pool.New(cfg, st)
	sp := &stubProvider{name: provName}
	pl.Register(sp)
	return New(cfg, pl, nil, nil), sp
}

func lastBody(t *testing.T, sp *stubProvider, wantModel string) map[string]any {
	t.Helper()
	if len(sp.got) == 0 {
		t.Fatal("上游没收到请求")
	}
	var req map[string]any
	if err := json.Unmarshal(sp.got[len(sp.got)-1], &req); err != nil {
		t.Fatalf("上游请求不是合法 JSON: %v", err)
	}
	if wantModel != "" && req["model"] != wantModel {
		t.Fatalf("model = %v, want %s", req["model"], wantModel)
	}
	return req
}

// 核心需求 1：破甲对所有供应商生效（这里用 trae，明确不是 ollama）。
func TestPJAppliesToEveryProvider(t *testing.T) {
	for _, provName := range []string{store.PTrae, store.PWorkBuddy, store.PZCode, store.PQwen, store.POllama} {
		g, _ := newStubGateway(t, provName)
		body := []byte(`{"model":"stub-model","messages":[{"role":"system","content":"原生提示"},{"role":"user","content":"帮我写个补丁"}]}`)
		out := g.applyPJModel(provName, "stub-model", "stub-model", body)
		if bytes.Equal(out, body) {
			t.Fatalf("%s：破甲没生效", provName)
		}
	}
}

// 核心需求 2：设置里关掉就完全不注入。
func TestPJOffInjectsNothing(t *testing.T) {
	g, _ := newStubGateway(t, store.PTrae)
	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)

	g.cfg.PJEnabled = false
	if got := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); !bytes.Equal(got, body) {
		t.Fatal("总开关关闭时不应注入")
	}

	g.cfg.PJEnabled = true
	g.cfg.PJMode = "off"
	if got := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); !bytes.Equal(got, body) {
		t.Fatal("策略 off 时不应注入")
	}
}

func TestPJScopeOllamaOnly(t *testing.T) {
	g, _ := newStubGateway(t, store.PTrae)
	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	g.cfg.PJScope = config.PJScopeOllama
	if got := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); !bytes.Equal(got, body) {
		t.Fatal("scope=ollama 时不应该打 trae")
	}
}

// 前置段策略：system 里出现破甲段，且原 system 保留。
func TestPJFrontStrategyEndToEnd(t *testing.T) {
	g, sp := newStubGateway(t, store.PZCode)
	g.cfg.PJMode = "armor_scope"
	body := []byte(`{"model":"stub-model","messages":[{"role":"system","content":"原生提示"},{"role":"user","content":"hi"}]}`)
	if err := g.pool.Chat(context.Background(), "stub-model", g.applyPJModel(store.PZCode, "stub-model", "stub-model", body), NewAggregateSink("stub-model")); err != nil {
		t.Fatalf("chat: %v", err)
	}
	req := lastBody(t, sp, "stub-model")
	msgs := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应注入一条 system，实际 %d 条", len(msgs))
	}
	if msgs[0].(map[string]any)["content"] != "原生提示" {
		t.Fatal("原 system 被改动")
	}
	if !strings.Contains(msgs[1].(map[string]any)["content"].(string), "operator_scope") {
		t.Fatal("破甲段没写进 system")
	}
}

// auto 模式：探测成功 → 记忆 → 后续请求直接用记忆策略、不再探测。
func TestPJAutoProbesAndRemembers(t *testing.T) {
	g, sp := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "auto"

	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"帮我分析这个二进制"}]}`)
	out := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body)
	if bytes.Equal(out, body) {
		t.Fatal("auto 探测成功后应注入")
	}

	// 探测用的是固定探测句，且第一轮探测就已经发了请求
	if len(sp.got) != 1 {
		t.Fatalf("首轮只应发 1 次探测（第一个策略就成功），实际 %d", len(sp.got))
	}
	var probe map[string]any
	json.Unmarshal(sp.got[0], &probe)
	if probe["stream"] != false {
		t.Fatal("探测必须关流")
	}
	// 探测策略 armor_scope 会在最前面补一条 system 破甲段，其余只有那一条探测消息
	if n := len(probe["messages"].([]any)); n != 1 && n != 2 {
		t.Fatalf("探测请求消息数异常: %d", n)
	}
	raw0, _ := json.Marshal(probe["messages"])
	if strings.Contains(string(raw0), "帮我分析这个二进制") {
		t.Fatal("探测不应把客户端的当轮内容发出去")
	}
	if !strings.Contains(string(raw0), "注入测试") {
		t.Fatalf("探测句不对: %s", raw0)
	}

	pick := g.PJAutoSnapshot()["stub-model"]
	if pick == "" || pick == pjAutoNone {
		t.Fatalf("记忆异常: %q", pick)
	}

	// 第二次请求：不再发探测，直接按记忆注入
	if err := g.pool.Chat(context.Background(), "stub-model",
		g.applyPJModel(store.PTrae, "stub-model", "stub-model", body), NewAggregateSink("stub-model")); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(sp.got) != 2 {
		t.Fatalf("已记忆后应只发 1 次真实请求，累计 %d", len(sp.got))
	}
	req := lastBody(t, sp, "stub-model")
	raw, _ := json.Marshal(req)
	if !strings.Contains(string(raw), "帮我分析这个二进制") {
		t.Fatal("真实请求内容丢失")
	}

	// 记忆落盘
	b, err := os.ReadFile(filepath.Join(g.cfg.DataDir, "pj_auto.json"))
	if err != nil || !strings.Contains(string(b), "stub-model") {
		t.Fatalf("记忆未落盘: %v %s", err, b)
	}

	// 重置后记忆清空
	if err := g.PJAutoReset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(g.PJAutoSnapshot()) != 0 {
		t.Fatal("重置后记忆应为空")
	}
}

// auto 模式：全部被判拒答 → 记 "-" → 之后透传，不重复浪费探测。
func TestPJAutoAllRefused(t *testing.T) {
	g, sp := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "auto"
	sp.refuse = true

	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	if got := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); !bytes.Equal(got, body) {
		t.Fatal("全部拒答时应透传原请求")
	}
	if g.PJAutoSnapshot()["stub-model"] != pjAutoNone {
		t.Fatalf("应记为 %q，实际 %q", pjAutoNone, g.PJAutoSnapshot()["stub-model"])
	}
	n := len(sp.got)
	// 已记为 "-"：这次真实请求照发（透传），但不再有额外的探测请求
	if err := g.pool.Chat(context.Background(), "stub-model",
		g.applyPJModel(store.PTrae, "stub-model", "stub-model", body), NewAggregateSink("stub-model")); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(sp.got) != n+1 {
		t.Fatalf("已记为无效后只应有一次真实请求，累计 %d（之前 %d）", len(sp.got), n)
	}
}

// auto 模式：探测本身报错（网络/额度）→ 不落库，下次还能重试。
func TestPJAutoProbeErrorDoesNotPoison(t *testing.T) {
	g, sp := newStubGateway(t, store.PTrae)
	g.cfg.PJMode = "auto"
	sp.failWith = provider.Err(provider.KindNetwork, 0, "boom")

	body := []byte(`{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	if got := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); !bytes.Equal(got, body) {
		t.Fatal("探测失败时应透传")
	}
	if len(g.PJAutoSnapshot()) != 0 {
		t.Fatal("探测失败不应写入记忆")
	}

	sp.failWith = nil
	if got := g.applyPJModel(store.PTrae, "stub-model", "stub-model", body); bytes.Equal(got, body) {
		t.Fatal("探测失败后应能重试成功")
	}
}
