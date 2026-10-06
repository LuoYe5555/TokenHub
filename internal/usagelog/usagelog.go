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
	TotalTokens      int64  `json:"totalTokens"`
	DurationMs       int64  `json:"durationMs"`
	Ok               bool   `json:"ok"`
	ErrMsg           string `json:"errMsg,omitempty"`
}

const maxEntries = 500

// Log 环形缓冲的用量日志（新条目在前），落盘 data/usagelog.json。
type Log struct {
	mu      sync.Mutex
	entries []Entry
	path    string
}

// Load 从数据目录加载（不存在则空日志）。
func Load(dataDir string) *Log {
	l := &Log{path: filepath.Join(dataDir, "usagelog.json")}
	if b, err := os.ReadFile(l.path); err == nil {
		var v struct {
			Entries []Entry `json:"entries"`
		}
		if json.Unmarshal(b, &v) == nil {
			l.entries = v.Entries
		}
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
	b, err := json.MarshalIndent(struct {
		Entries []Entry `json:"entries"`
	}{l.entries}, "", " ")
	if err == nil {
		tmp := l.path + ".tmp"
		if os.WriteFile(tmp, b, 0600) == nil {
			_ = os.Rename(tmp, l.path)
		}
	}
	l.mu.Unlock()
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
