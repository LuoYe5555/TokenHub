package gateway

// 破甲 auto 模式：按模型自动探测可用策略并记忆。
//
// 流程：某个模型第一次遇到 auto 策略时，把「客户端这一轮原本要问的那件事」
// 关掉 stream、依次套上 pj.Strategies() 里每个策略发给同一个提供商，用
// pj.Judge 给回复分类：
//   - 第一个判为 answer 的策略记进 data/pj_auto.json，之后这个模型的请求直接套它；
//   - 全部被拒 → 记成 "-"，后续该模型原样透传，不再浪费请求；
//   - 探测过程本身出错（网络/额度/凭据）→ 不落库，本次不注入，下次重试。
//
// 记忆按「客户端原始模型名」做 key（不含 provider: 前缀），跨账号、跨前缀稳定。
// 断言用的探测句固定为 pj.ProbePrompt：它属于破甲研究要观测的那类请求，
// 用客户端当轮内容去探测会污染用户看到的回复（探测结果不会回给客户端）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tokenhub/internal/logx"
	"tokenhub/internal/pj"
)

const pjAutoNone = "-" // 该模型探测过但全部被拒，不再注入

type pjAutoMem struct {
	once  sync.Once
	path  string
	mu    sync.Mutex
	picks map[string]string // model -> 策略 ID 或 pjAutoNone
}

func (g *Gateway) pjMemory() *pjAutoMem {
	g.pjAuto.once.Do(func() {
		path := filepath.Join(g.cfg.DataDir, "pj_auto.json")
		m := &pjAutoMem{path: path, picks: map[string]string{}}
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &m.picks)
		}
		g.pjAuto.picks = m
	})
	return g.pjAuto.picks
}

func (m *pjAutoMem) get(model string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.picks[model]
}

func (m *pjAutoMem) set(model, tpl string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.picks[model] = tpl
	if b, err := json.MarshalIndent(m.picks, "", "  "); err == nil {
		_ = os.WriteFile(m.path, b, 0o600)
	}
}

// PJAutoSnapshot 当前 auto 记忆（面板展示；实现 web.PJAutoAccessor）。
func (g *Gateway) PJAutoSnapshot() map[string]string {
	if g.cfg == nil || g.cfg.DataDir == "" {
		return map[string]string{}
	}
	mem := g.pjMemory()
	mem.mu.Lock()
	defer mem.mu.Unlock()
	out := make(map[string]string, len(mem.picks))
	for k, v := range mem.picks {
		out[k] = v
	}
	return out
}

// ResetPJAuto 清空 auto 记忆（面板「重新探测」按钮）。
func (g *Gateway) ResetPJAuto() error {
	if g.cfg == nil || g.cfg.DataDir == "" {
		return nil
	}
	mem := g.pjMemory()
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.picks = map[string]string{}
	if b, err := json.MarshalIndent(mem.picks, "", "  "); err == nil {
		return os.WriteFile(mem.path, b, 0o600)
	}
	return nil
}

// PJAutoReset 实现 web.PJAutoAccessor。
func (g *Gateway) PJAutoReset() error { return g.ResetPJAuto() }

// PJStatusSnapshot 破甲运行状态快照（实现 web.PJStatus）。
func (g *Gateway) PJStatusSnapshot() map[string]any {
	if g.cfg == nil {
		return nil
	}
	return map[string]any{
		"mode":    g.cfg.PJMode,
		"scope":   g.cfg.PJScope,
		"active":  g.cfg.PJOn(),
		"enabled": g.cfg.PJEnabled,
	}
}

// pjAutoApply auto 模式入口：查记忆 → 命中即套用；未探测过则逐策略探测。
// key 为客户端原始模型名；body 为原始请求 JSON（保留原 stream 设置）。
func (g *Gateway) pjAutoApply(key string, body []byte) []byte {
	if key == "" {
		return body
	}
	mem := g.pjMemory()
	switch pick := mem.get(key); {
	case pick == pjAutoNone:
		return body // 已知全部策略无效，透传
	case pick != "":
		if containsStrategy(pick) {
			logx.Infof("gateway", "破甲 auto：模型 %s 使用已记忆策略 %s", key, pick)
			return pj.ApplyStrategy(pick, body)
		}
	}

	// 未探测过：用固定探测句逐策略试（关流、单轮、最小请求）
	probe := buildPJProbe(key)
	if probe == nil {
		return body
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, strat := range pj.Strategies() {
		payload := pj.ApplyStrategy(strat, probe)
		if payload == nil {
			continue
		}
		sink := NewAggregateSink(key)
		if err := g.pool.Chat(ctx, key, payload, sink); err != nil {
			logx.Infof("gateway", "破甲 auto：模型 %s 探测（%s）调用失败，本次不注入: %v", key, strat, err)
			return body // 探测中断不落库，下次重试
		}
		raw, perr := sink.Result()
		if perr != nil {
			logx.Infof("gateway", "破甲 auto：模型 %s 探测（%s）返回错误，本次不注入: %s", key, strat, perr.Msg)
			return body
		}
		verdict := pj.Judge(openaiContentText(raw))
		logx.Infof("gateway", "破甲 auto：模型 %s 策略 %s 探测结果 %s", key, strat, verdict)
		if verdict == pj.VerdictAnswer {
			mem.set(key, strat)
			logx.Infof("gateway", "破甲 auto：模型 %s 自动选定策略 %s（已记忆，可在设置页重新探测）", key, strat)
			return pj.ApplyStrategy(strat, body)
		}
	}
	mem.set(key, pjAutoNone)
	logx.Infof("gateway", "破甲 auto：模型 %s 所有策略均被拒答，已记为不注入", key)
	return body
}

// containsStrategy 该 ID 是否在 auto 的策略清单里（含 armor_scope+3_liner 这类组合）。
func containsStrategy(id string) bool {
	for _, s := range pj.Strategies() {
		if s == id {
			return true
		}
	}
	return false
}

// buildPJProbe 构造固定探测请求：单条 user 消息 + 关流 + 小 max_tokens。
func buildPJProbe(model string) []byte {
	req := map[string]any{
		"model":      model,
		"stream":     false,
		"max_tokens": 512,
		"messages":   []any{map[string]any{"role": "user", "content": pj.ProbePrompt}},
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil
	}
	return b
}

// openaiContentText 从 OpenAI 非流式响应 JSON 里取首条回答文本。
func openaiContentText(raw []byte) string {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}
