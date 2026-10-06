// Package store 多账号凭据与运行状态持久化（data/accounts.json，0600 原子写）。
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	PWorkBuddy = "workbuddy"
	PTrae      = "trae"
	PZCode     = "zcode"
)

// QuotaPart 额度明细（权益包 / 额度窗口）。
type QuotaPart struct {
	Name      string  `json:"name"`
	Total     float64 `json:"total"`
	Used      float64 `json:"used"`
	Remain    float64 `json:"remain"`
	Unit      string  `json:"unit,omitempty"`
	Pending   bool    `json:"pending,omitempty"`
	ExpiresAt int64   `json:"expiresAt,omitempty"`
}

// QuotaSnapshot 一次额度查询的统一快照。
type QuotaSnapshot struct {
	Total     float64     `json:"total"`
	Used      float64     `json:"used"`
	Remain    float64     `json:"remain"`
	Unit      string      `json:"unit,omitempty"`
	Plan      string      `json:"plan,omitempty"`
	Source    string      `json:"source,omitempty"`
	Packs     int         `json:"packs,omitempty"`
	Parts     []QuotaPart `json:"parts,omitempty"`
	Note      string      `json:"note,omitempty"`
	UpdatedAt int64       `json:"updatedAt"`
}

// Account 一个第三方账号（凭据 + 运行状态）。
type Account struct {
	ID       string `json:"id"`
	Provider string `json:"provider"` // workbuddy | trae | zcode
	Realm    string `json:"realm,omitempty"` // workbuddy: cn|global；zcode: bigmodel|zai
	UID      string `json:"uid,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Email    string `json:"email,omitempty"`
	Enabled  bool   `json:"enabled"`

	// 凭据（WorkBuddy / Trae 为主凭据；ZCode 的 AccessToken = zcode JWT）
	AccessToken  string            `json:"accessToken,omitempty"`
	RefreshToken string            `json:"refreshToken,omitempty"`
	ExpiresAt    int64             `json:"expiresAt,omitempty"` // unix 秒
	Domain       string            `json:"domain,omitempty"`
	EnterpriseID string            `json:"enterpriseId,omitempty"`
	Extra        map[string]string `json:"extra,omitempty"` // provider 专属字段

	CreatedAt   int64 `json:"createdAt"`
	LastRefresh int64 `json:"lastRefresh,omitempty"`

	// 池状态（持久化，重启保留）
	CooldownUntil  int64  `json:"cooldownUntil,omitempty"`
	CooldownReason string `json:"cooldownReason,omitempty"`
	Dead           bool   `json:"dead,omitempty"`
	DeadReason     string `json:"deadReason,omitempty"`

	LastCheckinDay string         `json:"lastCheckinDay,omitempty"`
	LastCheckinAt  int64          `json:"lastCheckinAt,omitempty"`
	LastCheckinMsg string         `json:"lastCheckinMsg,omitempty"`
	LastQuota      *QuotaSnapshot `json:"lastQuota,omitempty"`
}

// Extra 键（各 provider 约定）
const (
	ExtraTraeMachineID = "machineId"
	ExtraTraeDeviceID  = "deviceId"
	ExtraTraeAPIHost   = "apiHost"
	// Trae 客户端登录信封（storage.json 里 tc 加密串原样保存，供一键切换回写 IDE）
	ExtraTraeAuthEnvelope   = "traeAuthEnv"   // iCubeAuthInfo://icube.cloudide
	ExtraTraeEntEnvelope    = "traeEntEnv"    // iCubeEntitlementInfo://icube.cloudide
	ExtraTraeServerEnvelope = "traeServerEnv" // iCubeServerData://icube.cloudide
	ExtraZCodeDeviceMid = "deviceMid"
	ExtraZCodeCredentials = "credentials" // 原始 credentials.json 内容（多为 enc:v1 密文）
	ExtraZCodeLoginProvider = "loginProvider"
	ExtraZCodeBigmodelAT  = "bigmodelAccessToken"
	ExtraZCodeBigmodelRT  = "bigmodelRefreshToken"
	ExtraZCodeZaiAT       = "zaiAccessToken"
)

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Store 账号集合（内存 + 磁盘）。
type Store struct {
	mu       sync.RWMutex
	path     string
	accounts []*Account
}

func Load(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "accounts.json")}
	if b, err := os.ReadFile(s.path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &s.accounts); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.accounts, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) All() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, a.Clone())
	}
	return out
}

func (s *Store) ByProvider(name string) []*Account {
	var out []*Account
	for _, a := range s.All() {
		if a.Provider == name {
			out = append(out, a)
		}
	}
	return out
}

func (s *Store) Get(id string) *Account {
	for _, a := range s.All() {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// Upsert 按 ID 插入或替换并落盘。
func (s *Store) Upsert(a *Account) error {
	s.mu.Lock()
	replaced := false
	for i, e := range s.accounts {
		if e.ID == a.ID {
			s.accounts[i] = a
			replaced = true
			break
		}
	}
	if !replaced {
		s.accounts = append(s.accounts, a)
	}
	s.mu.Unlock()
	return s.Save()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	for i, e := range s.accounts {
		if e.ID == id {
			s.accounts = append(s.accounts[:i], s.accounts[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	return s.Save()
}

// Mutate 原地修改账号（拷贝-写回-落盘）。
func (s *Store) Mutate(id string, fn func(a *Account)) error {
	s.mu.Lock()
	var target *Account
	for _, e := range s.accounts {
		if e.ID == id {
			target = e
			break
		}
	}
	s.mu.Unlock()
	if target == nil {
		return os.ErrNotExist
	}
	fn(target)
	return s.Save()
}

func (a *Account) Clone() *Account {
	cp := *a
	if a.Extra != nil {
		cp.Extra = make(map[string]string, len(a.Extra))
		for k, v := range a.Extra {
			cp.Extra[k] = v
		}
	}
	return &cp
}

func (a *Account) ExtraGet(k string) string {
	if a.Extra == nil {
		return ""
	}
	return a.Extra[k]
}

func (a *Account) ExtraSet(k, v string) {
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	a.Extra[k] = v
}

func (a *Account) NeedsRefresh(skew time.Duration) bool {
	if a.RefreshToken == "" || a.ExpiresAt <= 0 {
		return false
	}
	return time.Now().Unix() >= a.ExpiresAt-int64(skew.Seconds())
}

// DisplayedName 面板展示名。
func (a *Account) DisplayedName() string {
	if a.Nickname != "" {
		return a.Nickname
	}
	if a.Email != "" {
		return a.Email
	}
	if a.UID != "" {
		return a.UID
	}
	return a.ID
}
