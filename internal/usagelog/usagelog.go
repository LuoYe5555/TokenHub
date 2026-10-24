// Package usagelog 面板用量日志：记录每次 API 调用的 token 消耗（内存环形缓冲 + 本地持久化）。
package usagelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"tokenhub/internal/logx"
)

// Entry 一次 API 调用的用量记录。
type Entry struct {
	Time             int64   `json:"time"`   // unix 秒
	Caller           string  `json:"caller"` // 分享钥匙名称；空 = 主人（主密钥）
	Provider         string  `json:"provider"`
	Model            string  `json:"model"` // 请求里的原始模型名
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens,omitempty"`  // 缓存读取（上游计费包含）
	CacheWriteTokens int64   `json:"cacheWriteTokens,omitempty"` // 缓存写入（上游计费包含）
	TotalTokens      int64   `json:"totalTokens"`                // 输入+缓存读写+输出（上游计费口径）
	SavedTokens      int64   `json:"savedTokens,omitempty"`      // 网关层 token 优化节省的输入估算
	DurationMs       int64   `json:"durationMs"`
	Ok               bool    `json:"ok"`
	ErrMsg           string  `json:"errMsg,omitempty"`
	CostCny          float64 `json:"costCny,omitempty"` // 花费快照（CNY，落账时按当时价格表折算，跨币种已折）
	CostSrc          string  `json:"costSrc,omitempty"` // 价格来源：openrouter/llmabacus/builtin
}

// CacheHitRate 输入侧缓存命中率：cache_read ÷ (输入+cache_read+cache_write)。
func (e Entry) CacheHitRate() float64 {
	in := e.PromptTokens + e.CacheReadTokens + e.CacheWriteTokens
	if in <= 0 {
		return 0
	}
	return float64(e.CacheReadTokens) / float64(in)
}

const maxEntries = 500

// Daily 当日用量台账（按本地自然日累计，跨重启保留，不受环形缓冲裁剪影响）。
type Daily struct {
	Day        string  `json:"day"`    // 2006-01-02（本地时区）
	Tokens     int64   `json:"tokens"` // 计费总量（输入+缓存读写+输出）
	Reqs       int64   `json:"reqs"`
	InTokens   int64   `json:"inTokens,omitempty"`   // 未命中缓存的输入
	CacheRead  int64   `json:"cacheRead,omitempty"`  // 缓存读取
	CacheWrite int64   `json:"cacheWrite,omitempty"` // 缓存写入
	Saved      int64   `json:"saved,omitempty"`      // 网关层优化节省的输入估算
	Cost       float64 `json:"cost,omitempty"`       // 花费合计（CNY）
}

// CacheHitRate 当日输入侧缓存命中率。
func (d Daily) CacheHitRate() float64 {
	in := d.InTokens + d.CacheRead + d.CacheWrite
	if in <= 0 {
		return 0
	}
	return float64(d.CacheRead) / float64(in)
}

// Pricer 价格表最小接口（由 internal/pricing 实现，解耦避免循环依赖）。
type Pricer interface {
	Cost(model string, prompt, completion, cacheRead, cacheWrite int64) (cny float64, src string, ok bool)
	Rate() float64
}

// Log 环形缓冲的用量日志（新条目在前），落盘 data/usagelog.json。
type Log struct {
	mu      sync.Mutex
	entries []Entry
	daily   Daily
	// history 按天×模型的用量台账（面板趋势图/环形图用），保留约 60 天
	history map[string]map[string]*Daily
	path    string
	pricer  Pricer // 价格表（可选）：落账时快照花费
}

// SetPricer 注入价格表（启动时调用一次）。
func (l *Log) SetPricer(p Pricer) {
	l.mu.Lock()
	l.pricer = p
	l.mu.Unlock()
}

// Backfill 给没有价格快照的历史数据补计花费（一次性，幂等：只填 Cost==0 的记录）。
// 台账按天×模型聚合，完成量 = 总量 − 输入 − 缓存读写，分量齐全所以能精确回算。
func (l *Log) Backfill() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pricer == nil {
		return
	}
	n := 0
	// 历史台账
	for _, models := range l.history {
		for m, d := range models {
			if d.Cost != 0 || d.Tokens <= 0 {
				continue
			}
			out := d.Tokens - d.InTokens - d.CacheRead - d.CacheWrite
			if out < 0 {
				out = 0
			}
			if c, _, ok := l.pricer.Cost(m, d.InTokens, out, d.CacheRead, d.CacheWrite); ok {
				d.Cost = c
				n++
			}
		}
	}
	// 当日台账：跨模型聚合，不能按单一价回算 → 取今天台账各模型花费之和
	today := time.Now().Format("2006-01-02")
	if l.daily.Cost == 0 && l.daily.Tokens > 0 {
		var sum float64
		for _, d := range l.history[today] {
			sum += d.Cost
		}
		l.daily.Cost = sum
	}
	// 环形缓冲明细（今日卡片/明细列表的花费列）
	for i := range l.entries {
		e := &l.entries[i]
		if !e.Ok || e.CostCny != 0 || e.TotalTokens <= 0 {
			continue
		}
		if c, src, ok := l.pricer.Cost(e.Model, e.PromptTokens, e.CompletionTokens, e.CacheReadTokens, e.CacheWriteTokens); ok {
			e.CostCny, e.CostSrc = c, src
			n++
		}
	}
	l.persistLocked()
	if n > 0 {
		logx.Infof("usage", "已为 %d 条历史用量补计花费（按当前价格表回溯）", n)
	}
}

// Load 从数据目录加载（不存在则空日志）。
func Load(dataDir string) *Log {
	l := &Log{path: filepath.Join(dataDir, "usagelog.json")}
	if b, err := os.ReadFile(l.path); err == nil {
		var v struct {
			Entries []Entry                      `json:"entries"`
			Daily   *Daily                       `json:"daily"`
			History map[string]map[string]*Daily `json:"history"`
		}
		if json.Unmarshal(b, &v) == nil {
			l.entries = v.Entries
			if v.Daily != nil {
				l.daily = *v.Daily
			}
			l.history = v.History
		}
	}
	// 旧版数据没有 history：用环形缓冲里的明细回填一次（两者同文件原子落盘，不会重复计）
	if l.history == nil {
		l.history = map[string]map[string]*Daily{}
		for _, e := range l.entries {
			if e.Ok {
				l.addHistory(e)
			}
		}
	}
	// 跨天了就清零（当日累计只在当天有效）
	today := time.Now().Format("2006-01-02")
	if l.daily.Day != today {
		l.daily = Daily{Day: today}
	}
	return l
}

// addHistory 累加一条到按天×模型台账（调用方须持锁）。
func (l *Log) addHistory(e Entry) {
	day := time.Unix(e.Time, 0).Format("2006-01-02")
	mm := l.history[day]
	if mm == nil {
		mm = map[string]*Daily{}
		l.history[day] = mm
	}
	d := mm[e.Model]
	if d == nil {
		d = &Daily{Day: day}
		mm[e.Model] = d
	}
	d.Tokens += e.TotalTokens
	d.Reqs++
	d.InTokens += e.PromptTokens
	d.CacheRead += e.CacheReadTokens
	d.CacheWrite += e.CacheWriteTokens
	d.Saved += e.SavedTokens
	d.Cost += e.CostCny
}

// pruneHistory 台账保留策略：数据量很小（天×模型聚合行），不再裁剪，
// 以支持「全部」时间范围的统计。（调用方无须持锁，保留空实现仅为兼容。）
func (l *Log) pruneHistory() {}

// Add 追加一条记录（新条目插到最前），超出上限裁剪并落盘。
func (l *Log) Add(e Entry) {
	if e.Time == 0 {
		e.Time = time.Now().Unix()
	}
	// 花费快照：落账即按「当时」价格表折算成 CNY，历史条目不再受后续调价影响
	if e.Ok && e.TotalTokens > 0 && l.pricer != nil {
		if c, src, ok := l.pricer.Cost(e.Model, e.PromptTokens, e.CompletionTokens, e.CacheReadTokens, e.CacheWriteTokens); ok {
			e.CostCny, e.CostSrc = c, src
		}
	}
	l.mu.Lock()
	l.entries = append([]Entry{e}, l.entries...)
	if len(l.entries) > maxEntries {
		l.entries = l.entries[:maxEntries]
	}
	// 当日台账：只累计成功请求，跨天自动清零
	if e.Ok {
		today := time.Now().Format("2006-01-02")
		if l.daily.Day != today {
			l.daily = Daily{Day: today}
		}
		l.daily.Tokens += e.TotalTokens
		l.daily.Reqs++
		l.daily.InTokens += e.PromptTokens
		l.daily.CacheRead += e.CacheReadTokens
		l.daily.CacheWrite += e.CacheWriteTokens
		l.daily.Saved += e.SavedTokens
		l.daily.Cost += e.CostCny
		l.addHistory(e)
		l.pruneHistory()
	}
	l.persistLocked()
	l.mu.Unlock()
}

// persistLocked 落盘（调用方须持锁）。
func (l *Log) persistLocked() {
	b, err := json.MarshalIndent(struct {
		Entries []Entry                      `json:"entries"`
		Daily   Daily                        `json:"daily"`
		History map[string]map[string]*Daily `json:"history,omitempty"`
	}{l.entries, l.daily, l.history}, "", " ")
	if err == nil {
		tmp := l.path + ".tmp"
		if os.WriteFile(tmp, b, 0600) == nil {
			_ = os.Rename(tmp, l.path)
		}
	}
}

// Today 返回当日累计（tokens / 请求数）。跨天自动清零。
func (l *Log) Today() Daily {
	l.mu.Lock()
	defer l.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if l.daily.Day != today {
		l.daily = Daily{Day: today}
	}
	return l.daily
}

// AllTime 全历史累计（tokens / 请求数 / 花费 CNY，来自持久台账，不受环形缓冲 500 条影响）。
func (l *Log) AllTime() (tokens, reqs int64, cost float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, models := range l.history {
		for _, d := range models {
			tokens += d.Tokens
			reqs += d.Reqs
			cost += d.Cost
		}
	}
	return tokens, reqs, cost
}

// List 返回最近 limit 条（新在前）。
func (l *Log) List(limit int) []Entry {
	if limit <= 0 || limit > maxEntries {
		limit = maxEntries
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) > limit {
		out := make([]Entry, limit)
		copy(out, l.entries[:limit])
		return out
	}
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// ── 图表统计（近 N 天按日×模型） ──

// ModelStat 单模型近 N 天合计。
type ModelStat struct {
	Model  string  `json:"model"`
	Tokens int64   `json:"tokens"`
	Reqs   int64   `json:"reqs"`
	Cost   float64 `json:"cost,omitempty"` // 花费合计（CNY）
}

// DayStat 单日用量（含分模型明细）。
type DayStat struct {
	Day    string           `json:"day"`
	Tokens int64            `json:"tokens"`
	Saved  int64            `json:"saved,omitempty"` // 当日优化节省
	Models map[string]int64 `json:"models"`
}

// Stats 近 N 天趋势与模型占比。
type Stats struct {
	Days       []DayStat   `json:"days"`
	Models     []ModelStat `json:"models"`
	Total      int64       `json:"total"`
	Reqs       int64       `json:"reqs"`       // 范围内成功请求数
	SavedTotal int64       `json:"savedTotal"` // 近 N 天累计节省
	Cost       float64     `json:"cost"`       // 范围内花费合计（CNY）
}

// Stats 汇总近 days 天（含今天；无数据的日子也占位，保证 x 轴连续）。
// days<0 表示全部历史：只列有数据的日期（升序），不占位。
func (l *Log) Stats(days int) *Stats {
	all := days < 0
	if !all {
		if days <= 0 || days > 60 {
			days = 7
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	idx := map[string]int{}
	st := &Stats{}
	if all {
		ds := make([]string, 0, len(l.history))
		for day := range l.history {
			ds = append(ds, day)
		}
		sort.Strings(ds)
		st.Days = make([]DayStat, 0, len(ds))
		for _, k := range ds {
			idx[k] = len(st.Days)
			st.Days = append(st.Days, DayStat{Day: k, Models: map[string]int64{}})
		}
	} else {
		st.Days = make([]DayStat, 0, days)
		for i := days - 1; i >= 0; i-- {
			k := now.AddDate(0, 0, -i).Format("2006-01-02")
			idx[k] = len(st.Days)
			st.Days = append(st.Days, DayStat{Day: k, Models: map[string]int64{}})
		}
	}
	for day, models := range l.history {
		di, ok := idx[day]
		if !ok {
			continue
		}
		for m, d := range models {
			st.Days[di].Tokens += d.Tokens
			st.Days[di].Saved += d.Saved
			st.Days[di].Models[m] += d.Tokens
			st.Total += d.Tokens
			st.Reqs += d.Reqs
			st.SavedTotal += d.Saved
			st.Cost += d.Cost
		}
	}
	agg := map[string]*ModelStat{}
	for _, ds := range st.Days {
		for m, tk := range ds.Models {
			a := agg[m]
			if a == nil {
				a = &ModelStat{Model: m}
				agg[m] = a
			}
			a.Tokens += tk
		}
	}
	for day, models := range l.history {
		if _, ok := idx[day]; !ok {
			continue
		}
		for m, d := range models {
			agg[m].Reqs += d.Reqs
			agg[m].Cost += d.Cost
		}
	}
	for _, a := range agg {
		st.Models = append(st.Models, *a)
	}
	sort.Slice(st.Models, func(i, j int) bool { return st.Models[i].Tokens > st.Models[j].Tokens })
	return st
}
