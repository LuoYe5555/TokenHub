// Package config 持久化 TokenHub 配置（data 目录下 config.json）。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	AppName    = "TokenHub"
	AppVersion = "1.0.0"

	PWorkBuddy = "workbuddy"
	PTrae      = "trae"
	PZCode     = "zcode"
)

func AllProviders() []string { return []string{PWorkBuddy, PTrae, PZCode} }

// ProviderConfig 单个提供商的开关与上游地址覆盖（上游接口变更时使用）。
type ProviderConfig struct {
	Enabled      bool   `json:"enabled"`
	DefaultModel string `json:"defaultModel"`
	// WorkBuddy: APIBase=chat/oauth 域, BillingBase=计费域
	// Trae: AgentBase=对话域, APIBase=OAuth 域, BillingBase=ug 域, ConsoleBase=登录页域
	// ZCode: APIBase=zcode.z.ai, BillingBase=open.bigmodel.cn
	APIBase     string `json:"apiBase,omitempty"`
	BillingBase string `json:"billingBase,omitempty"`
	AgentBase   string `json:"agentBase,omitempty"`
	ConsoleBase string `json:"consoleBase,omitempty"`
}

// Config 全局配置。
type Config struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	APIKey      string `json:"apiKey"`
	OpenBrowser bool   `json:"openBrowser"`

	// 模型路由：裸模型名按 ModelRoutes 指定提供商，否则走 DefaultProvider；
	// 任何请求都可用 "provider:model" 前缀显式指定，例如 "trae:glm-5.2"。
	DefaultProvider string            `json:"defaultProvider"`
	ModelRoutes     map[string]string `json:"modelRoutes"`

	Providers map[string]*ProviderConfig `json:"providers"`

	// 自动领取
	AutoCheckin           bool  `json:"autoCheckin"`           // 每日签到（WorkBuddy CN / Trae）
	CheckinHours          []int `json:"checkinHours"`          // 每天这些整点尝试签到（当日已签自动跳过）
	WBIntlKeepalive       bool  `json:"wbIntlKeepalive"`       // WorkBuddy 国际版无签到，用一条极小对话保活
	ZCodeAutoClaim        bool  `json:"zcodeAutoClaim"`        // ZCode 活动套餐自动轮询领取
	ZCodeClaimIntervalMin int   `json:"zcodeClaimIntervalMin"` // ZCode 领取轮询周期（分钟）

	// ZCode 请求形状注入：overwrite=整体替换 system（最稳，移植自实测流量）；prepend=前置；off=不注入
	ZCodeInjectShape string `json:"zcodeInjectShape"`

	QuotaRefreshSec   float64 `json:"quotaRefreshSec"`   // 额度自动刷新周期（秒，支持 0.5 等小数），0=关闭
	TraeAutoSwitch    bool    `json:"traeAutoSwitch"`    // 定时刷新发现 Trae 积分耗尽时，自动冷却该号切换下一个
	RefreshSkewMin    int     `json:"refreshSkewMin"`    // token 剩余多少分钟内视为将过期，提前刷新
	MaxRotate         int     `json:"maxRotate"`         // 单次请求最多尝试的账号数
	UpstreamTimeoutSec int    `json:"upstreamTimeoutSec"` // 上游墙钟超时（SSE 长连接也用它兜底）
	ConnectTimeoutSec  int    `json:"connectTimeoutSec"`  // 连接段（首字节前）超时

	ExposePrefixModels bool `json:"exposePrefixModels"` // /v1/models 同时列出 "provider:model" 变体

	DataDir string `json:"-"`
}

func genKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "sk-th-" + hex.EncodeToString(b)
}

func Default() *Config {
	return &Config{
		Host:        "127.0.0.1",
		Port:        8687,
		APIKey:      genKey(),
		OpenBrowser: true,

		DefaultProvider: PWorkBuddy,
		ModelRoutes:     map[string]string{},

		Providers: map[string]*ProviderConfig{
			PWorkBuddy: {Enabled: true, DefaultModel: "deepseek-v4-pro"},
			PTrae:      {Enabled: true, DefaultModel: "glm-5.2"},
			PZCode:     {Enabled: true, DefaultModel: "glm-5.3"},
		},

		AutoCheckin:           true,
		CheckinHours:          []int{9, 21},
		WBIntlKeepalive:       false,
		ZCodeAutoClaim:        true,
		ZCodeClaimIntervalMin: 30,
		ZCodeInjectShape:      "overwrite",

		QuotaRefreshSec:    300,
		TraeAutoSwitch:     true,
		RefreshSkewMin:     1440,
		MaxRotate:          6,
		UpstreamTimeoutSec: 600,
		ConnectTimeoutSec:  15,

		ExposePrefixModels: true,
	}
}

// Load 读取配置；不存在则写入默认配置。dataDir 必须已存在或可创建。
func Load(dataDir string) (*Config, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	cfg := Default()
	p := filepath.Join(dataDir, "config.json")
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	}
	cfg.Normalize()
	cfg.DataDir = dataDir
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Normalize() {
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port <= 0 {
		c.Port = 8687
	}
	if c.APIKey == "" {
		c.APIKey = genKey()
	}
	if c.Providers == nil {
		c.Providers = map[string]*ProviderConfig{}
	}
	defModels := map[string]string{PWorkBuddy: "deepseek-v4-pro", PTrae: "glm-5.2", PZCode: "glm-5.3"}
	for _, name := range AllProviders() {
		pc := c.Providers[name]
		if pc == nil {
			pc = &ProviderConfig{Enabled: true}
			c.Providers[name] = pc
		}
		if pc.DefaultModel == "" {
			pc.DefaultModel = defModels[name]
		}
	}
	if c.DefaultProvider == "" {
		c.DefaultProvider = PWorkBuddy
	}
	if c.ModelRoutes == nil {
		c.ModelRoutes = map[string]string{}
	}
	if len(c.CheckinHours) == 0 {
		c.CheckinHours = []int{9, 21}
	}
	if c.ZCodeClaimIntervalMin <= 0 {
		c.ZCodeClaimIntervalMin = 30
	}
	switch c.ZCodeInjectShape {
	case "overwrite", "prepend", "off":
	default:
		c.ZCodeInjectShape = "overwrite"
	}
	if c.RefreshSkewMin <= 0 {
		c.RefreshSkewMin = 1440
	}
	if c.MaxRotate <= 0 {
		c.MaxRotate = 6
	}
	if c.UpstreamTimeoutSec <= 0 {
		c.UpstreamTimeoutSec = 600
	}
	if c.ConnectTimeoutSec <= 0 {
		c.ConnectTimeoutSec = 15
	}
	if c.QuotaRefreshSec < 0 {
		c.QuotaRefreshSec = 0
	}
	if c.QuotaRefreshSec > 0 && c.QuotaRefreshSec < 0.5 {
		c.QuotaRefreshSec = 0.5 // 最快 0.5 秒一轮，防止误填 0.01 打爆上游
	}
}

func (c *Config) Save() error {
	p := filepath.Join(c.DataDir, "config.json")
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// PC 返回指定提供商配置（保证非 nil）。
func (c *Config) PC(name string) *ProviderConfig {
	if c.Providers == nil {
		c.Providers = map[string]*ProviderConfig{}
	}
	if pc, ok := c.Providers[name]; ok && pc != nil {
		return pc
	}
	pc := &ProviderConfig{Enabled: true}
	c.Providers[name] = pc
	return pc
}

func (c *Config) UpstreamTimeout() time.Duration {
	return time.Duration(c.UpstreamTimeoutSec) * time.Second
}

func (c *Config) ConnectTimeout() time.Duration {
	return time.Duration(c.ConnectTimeoutSec) * time.Second
}

// QuotaRefresh 额度刷新周期；0 表示关闭。
func (c *Config) QuotaRefresh() time.Duration {
	if c.QuotaRefreshSec <= 0 {
		return 0
	}
	return time.Duration(c.QuotaRefreshSec * float64(time.Second))
}
