// Package usagelog 面板用量日志：记录每次 API 调用的 token 消耗（内存环形缓冲 + 本地持久化）。
package usagelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 一次 API 调用的用量记录。
type Entry struct {
	Time             int64  `json:"time"` // unix 秒
	Caller           string `json:"caller"`            // 分享钥匙名称；空 = 主人（主密钥）
	Provider         string `json:"provider"`
	Model            string `json:"model"` // 请求里的原始模型名
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens,omitempty"`  // 缓存读取（上游计费包含）
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"` // 缓存写入（上游计费包含）
	TotalTokens      int64  `json:"totalTokens"`                // 输入+缓存读写+输出（上游计费口径）
	DurationMs       int64  `json:"durationMs"`
	Ok               bool   `json:"ok"`
	ErrMsg           string `json:"errMsg,omitempty"`
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
	Day        string `json:"day"`                  // 2006-01-02（本地时区）
	Tokens     int64  `json:"tokens"`               // 计费总量（输入+缓存读写+输出）
	Reqs       int64  `json:"reqs"`
	InTokens   int64  `json:"inTokens,omitempty"`   // 未命中缓存的输入
	CacheRead  int64  `json:"cacheRead,omitempty"`  // 缓存读取
	CacheWrite int64  `json:"cacheWrite,omitempty"` // 缓存写入
}

// CacheHitRate 当日输入侧缓存命中率。
func (d Daily) CacheHitRate() float64 {
	in := d.InTokens + d.CacheRead + d.CacheWrite
	if in <= 0 {
		return 0
	}
	return float64(d.CacheRead) / float64(in)
}

// Log 环形缓冲的用量日志（新条目在前），落盘 data/usagelog.json。
type Log struct {
	mu      sync.Mutex
	entries []Entry
	daily   Daily
	path    string
}

// Load 从数据目录加载（不存在则空日志）。
func Load(dataDir string) *Log {
	l := &Log{path: filepath.Join(dataDir, "usagelog.json")}
	if b, err := os.ReadFile(l.path); err == nil {
		var v struct {
			Entries []Entry `json:"entries"`
			Daily   *Daily  `json:"daily"`
		}
		if json.Unmarshal(b, &v) == nil {
			l.entries = v.Entries
			if v.Daily != nil {
				l.daily = *v.Daily
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

// Add 追加一条记录（新条目插到最前），超出上限裁剪并落盘。
func (l *Log) Add(e Entry) {
	if e.Time == 0 {
		e.Time = time.Now().Unix()
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
	}
	l.persistLocked()
	l.mu.Unlock()
}

// persistLocked 落盘（调用方须持锁）。
func (l *Log) persistLocked() {
	b, err := json.MarshalIndent(struct {
		Entries []Entry `json:"entries"`
		Daily   Daily   `json:"daily"`
	}{l.entries, l.daily}, "", " ")
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
