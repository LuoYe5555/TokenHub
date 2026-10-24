// Package pricing 大模型价格表：用于把用量日志换算成钱。
//
// 价格口径：每百万 tokens。两条数据源（至少每日一更新）：
//   - OpenRouter  https://openrouter.ai/api/v1/models   （USD，覆盖海外模型）
//   - LLMabacus   https://www.llmabacus.com/api/prices  （CNY，覆盖国产模型，llm-prices-cn 同源）
//
// 离线兜底：内置一份构建时的国产模型 CNY 快照（builtin_cn.json，go:embed）。
// 「使用时的价格」：usagelog.Add 落账时即按当时价格表换算并快照进条目，
// 之后价格表更新不影响历史记录。
package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/logx"
)

// ModelPrice 单模型价格（每百万 tokens）。
type ModelPrice struct {
	ID         string  `json:"id"`               // 源原始 id（含厂商前缀，如 z-ai/glm-5.3）
	Name       string  `json:"name,omitempty"`   // 显示名
	Vendor     string  `json:"vendor,omitempty"` // 厂商（中文优先）
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
	Currency   string  `json:"currency"` // USD / CNY
	Source     string  `json:"source"`   // openrouter / llmabacus / builtin
}

// Table 价格表（并发安全，落盘 data/prices.json）。
type Table struct {
	mu        sync.Mutex
	path      string
	prices    map[string]*ModelPrice // key = normalize(id)
	updatedAt int64                  // 最近一次联网刷新 unix 秒（0 = 从未联网）
	rate      float64                // USD→CNY
	refreshed time.Time              // 上次尝试刷新时间（含失败，用于节流）
}

//go:embed builtin_cn.json
var builtinCN []byte

const staleAfter = 24 * time.Hour // 至少每日一更新

// Load 从数据目录加载价格表缓存；过期或缺失时后台触发一次刷新。
func Load(dataDir string) *Table {
	t := &Table{path: filepath.Join(dataDir, "prices.json"), rate: 7.2}
	t.loadLocked()
	// 内置兜底：只补缺，不覆盖已缓存/已刷新的条目
	t.mergeBuiltin()
	return t
}

// Start 启动每日刷新循环（后台 goroutine，随 ctx 退出）。
func (t *Table) Start(ctx context.Context) {
	go func() {
		t.MaybeRefresh(ctx) // 启动时过期就先刷
		tk := time.NewTicker(staleAfter)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				t.MaybeRefresh(ctx)
			}
		}
	}()
}

func (t *Table) loadLocked() {
	if b, err := os.ReadFile(t.path); err == nil {
		var v struct {
			UpdatedAt int64                  `json:"updatedAt"`
			Rate      float64                `json:"rate"`
			Models    map[string]*ModelPrice `json:"models"`
		}
		if json.Unmarshal(b, &v) == nil && len(v.Models) > 0 {
			t.prices = v.Models
			t.updatedAt = v.UpdatedAt
			if v.Rate > 1 {
				t.rate = v.Rate
			}
		}
	}
	if t.prices == nil {
		t.prices = map[string]*ModelPrice{}
	}
}

func (t *Table) mergeBuiltin() {
	var v struct {
		Rate   float64      `json:"rate"`
		Models []ModelPrice `json:"models"`
	}
	if json.Unmarshal(builtinCN, &v) != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if v.Rate > 1 && t.updatedAt == 0 {
		t.rate = v.Rate
	}
	for i := range v.Models {
		m := v.Models[i]
		m.Source = "builtin"
		k := normalize(m.ID)
		if _, ok := t.prices[k]; !ok {
			cp := m
			t.prices[k] = &cp
		}
	}
}

// normalize 模型名归一化：剥网关提供商前缀（zcode:/qw:/ol: 等），统一小写与分隔符。
// glm-5.3-flash → glm-5-3-flash；z-ai/glm-5.3 → 保留整体与尾段两种键候选。
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// 剥掉网关层前缀（首个冒号前是已知提供商名时）
	if i := strings.IndexByte(s, ':'); i > 0 {
		switch s[:i] {
		case "zcode", "zcode-plan", "trae", "wb", "workbuddy", "qw", "qwen", "ol", "ollama":
			s = s[i+1:]
		}
	}
	s = strings.NewReplacer(".", "-", "_", "-", " ", "", "/", "-", ":", "-").Replace(s)
	// 折叠连续 '-'
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// hasPrice 条目是否带有效价格（0/0 的占位条目不能用来计价）。
func hasPrice(p *ModelPrice) bool { return p != nil && (p.Input > 0 || p.Output > 0) }

// lookup 按 id / 尾段（vendor/xxx 的 xxx）/ 前缀包含 依次匹配；跳过 0 价占位条目。
func (t *Table) lookup(model string) *ModelPrice {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := normalize(model)
	if hasPrice(t.prices[key]) {
		return t.prices[key]
	}
	// vendor/xxx 的尾段
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		if hasPrice(t.prices[key[i+1:]]) {
			return t.prices[key[i+1:]]
		}
	}
	// 模糊：请求名是某候选的前缀，或候选是请求名的前缀（取最长公共匹配）
	var best *ModelPrice
	bestLen := 0
	for k, p := range t.prices {
		if !hasPrice(p) {
			continue
		}
		var n int
		switch {
		case strings.HasPrefix(key, k) || strings.HasPrefix(k, key):
			n = min(len(k), len(key))
			// 要求前缀至少 6 字符且后随字符是分隔符或结尾，避免 "glm-3" 撞 "glm-30"
			if n < 6 {
				continue
			}
			if n < len(k) && k[n] != '-' {
				continue
			}
			if n < len(key) && key[n] != '-' && n != len(key) {
				continue
			}
		default:
			continue
		}
		if n > bestLen {
			bestLen = n
			best = p
		}
	}
	return best
}

// Cost 按当前价格表估算一次调用的花费（CNY，跨币种按表内汇率折算）。
func (t *Table) Cost(model string, prompt, completion, cacheRead, cacheWrite int64) (float64, string, bool) {
	p := t.lookup(model)
	if p == nil || (p.Input == 0 && p.Output == 0) {
		return 0, "", false
	}
	in, out, cr, cw := float64(prompt), float64(completion), float64(cacheRead), float64(cacheWrite)
	cost := in*p.Input + out*p.Output
	if cr > 0 {
		if p.CacheRead > 0 {
			cost += cr * p.CacheRead
		} else {
			cost += cr * p.Input // 无缓存价按输入价
		}
	}
	if cw > 0 {
		if p.CacheWrite > 0 {
			cost += cw * p.CacheWrite
		} else {
			cost += cw * p.Input
		}
	}
	cost /= 1e6
	t.mu.Lock()
	r := t.rate
	t.mu.Unlock()
	if p.Currency == "USD" {
		cost *= r
	}
	return cost, p.Source, true
}

// Rate 返回 USD→CNY 汇率（$ 展示用）。
func (t *Table) Rate() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rate <= 1 {
		return 7.2
	}
	return t.rate
}

// Meta 价格页元信息。
func (t *Table) Meta() (updatedAt int64, rate float64, count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.updatedAt, t.rate, len(t.prices)
}

// All 返回全部价格（按厂商+输入价排序，价格页用）。
func (t *Table) All() []ModelPrice {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ModelPrice, 0, len(t.prices))
	for _, p := range t.prices {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		if out[i].Input != out[j].Input {
			return out[i].Input < out[j].Input
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Refresh 强制联网刷新（OpenRouter + LLMabacus），落盘并更新表。
func (t *Table) Refresh(ctx context.Context) error {
	t.mu.Lock()
	if time.Since(t.refreshed) < 2*time.Minute {
		t.mu.Unlock()
		return nil // 节流：避免面板手滑连点
	}
	t.refreshed = time.Now()
	t.mu.Unlock()

	var wg sync.WaitGroup
	var orErr, abErr error
	wg.Add(2)
	go func() { defer wg.Done(); orErr = t.fetchOpenRouter(ctx) }()
	go func() { defer wg.Done(); abErr = t.fetchLLMabacus(ctx) }()
	wg.Wait()

	t.mu.Lock()
	t.updatedAt = time.Now().Unix()
	t.persistLocked()
	n := len(t.prices)
	t.mu.Unlock()

	if orErr != nil && abErr != nil {
		return fmt.Errorf("两个价格源都失败: openrouter=%v; llmabacus=%v", orErr, abErr)
	}
	logx.Infof("pricing", "价格表已刷新：%d 个模型（openrouter=%v llmabacus=%v）", n, errStr(orErr), errStr(abErr))
	return nil
}

func errStr(err error) string {
	if err != nil {
		return "失败: " + err.Error()
	}
	return "ok"
}

// MaybeRefresh 距上次成功刷新超过 24h 才刷。
func (t *Table) MaybeRefresh(ctx context.Context) {
	up, _, _ := t.Meta()
	if up > 0 && time.Since(time.Unix(up, 0)) < staleAfter {
		return
	}
	if err := t.Refresh(ctx); err != nil {
		logx.Warnf("pricing", "价格表刷新失败（继续用缓存/内置价）: %v", err)
	}
}

// fetchOpenRouter 拉 OpenRouter 模型价（USD/token → 每百万）。
func (t *Table) fetchOpenRouter(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := httpx.Do(cctx, "GET", "https://openrouter.ai/api/v1/models", nil, nil, 45*time.Second)
	if err != nil {
		return err
	}
	if resp.Status >= 400 {
		return fmt.Errorf("status %d", resp.Status)
	}
	var v struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Pricing struct {
				Prompt          string `json:"prompt"`
				Completion      string `json:"completion"`
				InputCacheRead  string `json:"input_cache_read"`
				InputCacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		return err
	}
	got := map[string]*ModelPrice{}
	for _, d := range v.Data {
		if d.ID == "" || d.Pricing.Prompt == "" {
			continue
		}
		mp := &ModelPrice{
			ID: d.ID, Name: d.Name, Currency: "USD", Source: "openrouter",
			Input:      num(d.Pricing.Prompt) * 1e6,
			Output:     num(d.Pricing.Completion) * 1e6,
			CacheRead:  num(d.Pricing.InputCacheRead) * 1e6,
			CacheWrite: num(d.Pricing.InputCacheWrite) * 1e6,
		}
		got[normalize(d.ID)] = mp
		// 尾段键：z-ai/glm-5.3 → "glm-5-3"（请求侧多半不带厂商前缀）
		if i := strings.LastIndexByte(d.ID, '/'); i >= 0 {
			if _, exists := got[keySeg(d.ID[i+1:])]; !exists {
				got[keySeg(d.ID[i+1:])] = mp
			}
		}
	}
	t.mu.Lock()
	for k, p := range got {
		// 已有带有效价的条目（含国产 CNY 源/内置）不让位；0 价占位条目可被覆盖
		if hasPrice(t.prices[k]) {
			continue
		}
		t.prices[k] = p
	}
	t.mu.Unlock()
	return nil
}

// fetchLLMabacus 拉国产模型价（llmabacus 线上 API：camelCase 字段，CNY/百万 + 汇率）。
func (t *Table) fetchLLMabacus(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := httpx.Do(cctx, "GET", "https://www.llmabacus.com/api/prices", nil, nil, 45*time.Second)
	if err != nil {
		return err
	}
	if resp.Status >= 400 {
		return fmt.Errorf("status %d", resp.Status)
	}
	var v struct {
		Meta struct {
			USDToCny float64 `json:"usdToCny"`
		} `json:"meta"`
		Vendors []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"vendors"`
		Models []struct {
			ID               string   `json:"id"`
			Name             string   `json:"name"`
			VendorID         string   `json:"vendorId"`
			InputPrice       *float64 `json:"inputPrice"`
			OutputPrice      *float64 `json:"outputPrice"`
			CachedInputPrice *float64 `json:"cachedInputPrice"`
			PriceCurrency    string   `json:"priceCurrency"`
		} `json:"models"`
	}
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		return err
	}
	vendorName := map[string]string{}
	for _, vd := range v.Vendors {
		vendorName[vd.ID] = vd.Name
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if v.Meta.USDToCny > 1 {
		t.rate = v.Meta.USDToCny
	}
	n := 0
	for _, m := range v.Models {
		if m.ID == "" || (m.InputPrice == nil && m.OutputPrice == nil) {
			continue
		}
		var in, out, cr float64
		if m.InputPrice != nil {
			in = *m.InputPrice
		}
		if m.OutputPrice != nil {
			out = *m.OutputPrice
		}
		if m.CachedInputPrice != nil {
			cr = *m.CachedInputPrice
		}
		cur := m.PriceCurrency
		if cur == "" {
			cur = "CNY"
		}
		mp := &ModelPrice{
			ID: m.ID, Name: m.Name, Vendor: vendorName[m.VendorID],
			Input: in, Output: out,
			CacheRead: cr, CacheWrite: cr * 1.25,
			Currency: cur, Source: "llmabacus",
		}
		// 0 价占位（官方未公布）不覆盖已有有效价
		if !hasPrice(mp) && hasPrice(t.prices[normalize(m.ID)]) {
			continue
		}
		t.prices[normalize(m.ID)] = mp
		n++
	}
	if n == 0 {
		return fmt.Errorf("返回 0 条模型价格")
	}
	return nil
}

// persistLocked 落盘（调用方须持锁）。
func (t *Table) persistLocked() {
	b, err := json.MarshalIndent(struct {
		UpdatedAt int64                  `json:"updatedAt"`
		Rate      float64                `json:"rate"`
		Models    map[string]*ModelPrice `json:"models"`
	}{t.updatedAt, t.rate, t.prices}, "", " ")
	if err == nil {
		tmp := t.path + ".tmp"
		if os.WriteFile(tmp, b, 0600) == nil {
			_ = os.Rename(tmp, t.path)
		}
	}
}

func num(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f
}

func keySeg(s string) string { return normalize(s) }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
