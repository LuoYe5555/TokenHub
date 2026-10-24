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
	PQwen      = "qwen"
	POllama    = "ollama"
)

func AllProviders() []string { return []string{PWorkBuddy, PTrae, PZCode, PQwen, POllama} }

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
	ZCodeToolHint    bool   `json:"zcodeToolHint,omitempty"` // 实验性：注入本地工具引导（可能触发上游 3007，默认关）
	ZCodeTSHint      bool   `json:"zcodeTsHint,omitempty"`   // 实验性：给 ToolSearch 描述追加"内置工具已加载"说明（是否触发 3007 待验证，默认关）

	QuotaRefreshSec    float64 `json:"quotaRefreshSec"`    // 额度自动刷新周期（秒，支持 0.5 等小数），0=关闭
	TraeAutoSwitch     bool    `json:"traeAutoSwitch"`     // 定时刷新发现 Trae 积分耗尽时，自动冷却该号切换下一个
	RefreshSkewMin     int     `json:"refreshSkewMin"`     // token 剩余多少分钟内视为将过期，提前刷新
	MaxRotate          int     `json:"maxRotate"`          // 单次请求最多尝试的账号数
	UpstreamTimeoutSec int     `json:"upstreamTimeoutSec"` // 上游墙钟超时（SSE 长连接也用它兜底）
	ConnectTimeoutSec  int     `json:"connectTimeoutSec"`  // 连接段（首字节前）超时

	ExposePrefixModels bool `json:"exposePrefixModels"` // /v1/models 同时列出 "provider:model" 变体

	// Token 优化（网关层无损省 token，不改变模型可见信息）
	OptDedup       *bool `json:"optDedup,omitempty"`       // 折叠会话内重复的大段内容（保留最新一份）；nil=开
	OptCompactJSON *bool `json:"optCompactJSON,omitempty"` // 紧凑化请求中的 pretty JSON；nil=开
	OptNearDup     *bool `json:"optNearDup,omitempty"`     // 近重复工具结果折叠（同一工具、内容≥90%相似的旧结果只留最新）；nil=开

	ReqDump bool `json:"reqDump,omitempty"` // 诊断：把客户端原始请求快照落盘 data/reqdump（默认关）

	// 破甲实验（提示词对抗研究）：off=默认关闭；仅对本地 Ollama 提供商生效（写死，不受路由影响）
	PJMode string `json:"pjMode"`

	// 网络加速：本地加速代理（DNS 优选+连接竞速）+ 系统代理一键接管；true=随程序自启
	Accel bool `json:"accel"`

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
		ZCodeInjectShape:      "prepend",
		PJMode:                "off",

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
	switch c.PJMode {
	case "off", "auto", "3_liner", "antigpt", "betterdan", "aim":
	default:
		c.PJMode = "off"
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

// OptDedupOn 会话内重复内容折叠开关（缺省开）。
func (c *Config) OptDedupOn() bool { return c.OptDedup == nil || *c.OptDedup }

// OptCompactJSONOn JSON 紧凑化开关（缺省开）。
func (c *Config) OptCompactJSONOn() bool { return c.OptCompactJSON == nil || *c.OptCompactJSON }

// OptNearDupOn 近重复工具结果折叠开关（缺省开）。
func (c *Config) OptNearDupOn() bool { return c.OptNearDup == nil || *c.OptNearDup }

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
