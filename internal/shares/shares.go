// Package shares 分享钥匙：可按提供商开关、可限额的对外调用凭据
// （data/shares.json，0600 原子写）。
//
// 安全约定：公网隧道只暴露带 Bearer 鉴权的 API；分享钥匙由本机源头控制
// （按提供商开关、按用量上限、随时停用），主 API Key 不外泄。
// 计量口径：token 用量（上游 usage 返回；流式无 usage 时按字符估算）。
// WorkBuddy / Trae 的「积分」无法逐请求获取，面板用 token 数近似控制，
// 建议把上限设为保守值。
package shares

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProvShare 单个提供商的分享配置与用量。
type ProvShare struct {
	Enabled    bool     `json:"enabled"`
	Limit      int64    `json:"limit"`            // token 上限，0 = 不限
	Models     []string `json:"models,omitempty"` // 允许的模型白名单（裸模型名），空 = 不限
	UsedTokens int64    `json:"usedTokens"`       // 累计 token 用量
	UsedReqs   int64    `json:"usedReqs"`         // 累计请求次数
}

// Share 一把分享钥匙。
type Share struct {
	ID      string                `json:"id"`
	Name    string                `json:"name"`
	Key     string                `json:"key"` // sk-share-xxxx
	Enabled bool                  `json:"enabled"`
	Prov    map[string]*ProvShare `json:"providers"` // workbuddy | trae | zcode
	// 历史字段（仅展示）
	LastUsedAt int64 `json:"lastUsedAt,omitempty"`
	CreatedAt  int64 `json:"createdAt"`
}

// Manager 分享钥匙集合（内存 + 磁盘）。
type Manager struct {
	mu     sync.Mutex
	path   string
	shares []*Share
}

func Load(dataDir string) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{path: filepath.Join(dataDir, "shares.json")}
	if b, err := os.ReadFile(m.path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &m.shares); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.shares, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newShareKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-share-" + hex.EncodeToString(b), nil
}

func defaultProv(enabled bool) *ProvShare {
	return &ProvShare{Enabled: enabled}
}

// All 列出全部分享钥匙。
func (m *Manager) All() []*Share {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Share, 0, len(m.shares))
	for _, s := range m.shares {
		cp := *s
		out = append(out, &cp)
	}
	return out
}

// Create 新建分享钥匙。provs: 提供商名称 → 配置。
func (m *Manager) Create(name string, provs map[string]*ProvShare) (*Share, error) {
	key, err := newShareKey()
	if err != nil {
		return nil, err
	}
	s := &Share{
		ID:        NewID(),
		Name:      name,
		Key:       key,
		Enabled:   true,
		Prov:      map[string]*ProvShare{},
		CreatedAt: time.Now().Unix(),
	}
	for k, v := range provs {
		if v == nil {
			v = defaultProv(false)
		}
		cp := *v
		s.Prov[k] = &cp
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shares = append(m.shares, s)
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	cp := *s
	return &cp, nil
}

// Update 修改分享钥匙（拷贝-写回-落盘）。
func (m *Manager) Update(id string, fn func(s *Share)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *Share
	for _, e := range m.shares {
		if e.ID == id {
			target = e
			break
		}
	}
	if target == nil {
		return os.ErrNotExist
	}
	fn(target)
	if target.Prov == nil {
		target.Prov = map[string]*ProvShare{}
	}
	return m.saveLocked()
}

// Delete 删除分享钥匙。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.shares {
		if e.ID == id {
			m.shares = append(m.shares[:i], m.shares[i+1:]...)
			return m.saveLocked()
		}
	}
	return os.ErrNotExist
}

// Resolve 按钥匙串查找（只读，不计数）。
// 返回 (钥匙, 是否有效启用)。
func (m *Manager) Resolve(key string) (*Share, bool) {
	if key == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.shares {
		if e.Key == key {
			cp := *e
			return &cp, e.Enabled
		}
	}
	return nil, false
}

// Authorize 分享钥匙对某提供商的请求放行检查（通过则计一次请求）。
// model 为请求的裸模型名（不带 provider: 前缀）。
// 返回错误文案（中文），nil 表示放行。
func (m *Manager) Authorize(shareID, prov, model string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *Share
	for _, e := range m.shares {
		if e.ID == shareID {
			target = e
			break
		}
	}
	if target == nil {
		return "分享钥匙不存在或已被删除"
	}
	if !target.Enabled {
		return "该分享钥匙已被停用"
	}
	ps := target.Prov[prov]
	if ps == nil {
		ps = defaultProv(false)
		target.Prov[prov] = ps
	}
	if !ps.Enabled {
		return "该分享钥匙未开放 " + prov
	}
	// 模型白名单：空 = 全部允许
	if len(ps.Models) > 0 && !containsModel(ps.Models, model) {
		return fmt.Sprintf("该分享钥匙在 %s 下仅允许使用模型：%s", prov, strings.Join(ps.Models, "、"))
	}
	if ps.Limit > 0 && ps.UsedTokens >= ps.Limit {
		return "该分享钥匙在 " + prov + " 的用量已达上限"
	}
	ps.UsedReqs++
	target.LastUsedAt = time.Now().Unix()
	if err := m.saveLocked(); err != nil {
		_ = err
	}
	return ""
}

// containsModel 白名单匹配（忽略大小写与空格）。
func containsModel(whitelist []string, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, w := range whitelist {
		if strings.ToLower(strings.TrimSpace(w)) == model {
			return true
		}
	}
	return false
}

// Record 记录一次请求的 token 用量。
func (m *Manager) Record(shareID, prov string, tokens int64) {
	if shareID == "" || tokens <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.shares {
		if e.ID != shareID {
			continue
		}
		ps := e.Prov[prov]
		if ps == nil {
			ps = defaultProv(true)
			e.Prov[prov] = ps
		}
		ps.UsedTokens += tokens
		if err := m.saveLocked(); err != nil {
			_ = err
		}
		return
	}
}
