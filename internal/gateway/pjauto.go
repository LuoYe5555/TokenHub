package gateway

// 破甲 auto 模式：按模型自动探测有效模板并记忆。
// 流程：首次遇到某本地 Ollama 模型时，把用户当次请求（stream 关掉）依次套上
// 每个内置模板发给本机模型，用 pj.LooksRefused 判断是否拒答；第一个不拒答的
// 模板记入 data/pj_auto.json，之后该模型的请求直接套用，不再重复探测。
// 全部模板都被拒则记 "-"，后续请求原样透传（模型抗性太强，不再浪费算力）。
// 边界不变：仅本地 Ollama 提供商会走到这里（applyPJ 已写死）。

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
	picks map[string]string // model -> 模板 ID 或 pjAutoNone
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

// pjAutoApply auto 模式入口：查记忆 → 命中即套用；未探测过则逐模板探测。
// body 为原始请求 JSON（保留原 stream 设置）。
func (g *Gateway) pjAutoApply(body []byte) []byte {
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	model, _ := req["model"].(string)
	if model == "" {
		return body
	}
	mem := g.pjMemory()
	switch pick := mem.get(model); {
	case pick == pjAutoNone:
		return body // 已知全部模板无效，透传
	case pj.Get(pick) != nil:
		logx.Infof("gateway", "破甲 auto：模型 %s 使用已记忆模板 %s", model, pick)
		return pj.Apply(pick, body)
	}

	// 未探测过：关流逐模板试，选第一个不拒答的（只发本机 Ollama，不上网）
	probeBody, err := json.Marshal(req)
	if err == nil {
		var probeReq map[string]any
		if json.Unmarshal(probeBody, &probeReq) == nil {
			probeReq["stream"] = false
			probeBody, err = json.Marshal(probeReq)
		}
	}
	if err != nil {
		return body
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, t := range pj.Templates {
		sink := NewAggregateSink(model)
		if err := g.pool.Chat(ctx, model, pj.Apply(t.ID, probeBody), sink); err != nil {
			logx.Infof("gateway", "破甲 auto：模型 %s 探测（模板 %s）调用失败，本次不注入: %v", model, t.ID, err)
			return body // 探测中断不落库，下次重试
		}
		raw, perr := sink.Result()
		if perr != nil {
			logx.Infof("gateway", "破甲 auto：模型 %s 探测（模板 %s）返回错误，本次不注入: %s", model, t.ID, perr.Msg)
			return body
		}
		text := openaiContentText(raw)
		if text != "" && !pj.LooksRefused(text) {
			mem.set(model, t.ID)
			logx.Infof("gateway", "破甲 auto：模型 %s 自动选定模板 %s（已记忆，可用 data/pj_auto.json 重置）", model, t.ID)
			return pj.Apply(t.ID, body)
		}
	}
	mem.set(model, pjAutoNone)
	logx.Infof("gateway", "破甲 auto：模型 %s 所有模板均被拒答，已记为不注入", model)
	return body
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
