// Package provider 定义统一的多提供商接口与共享类型。
package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"tokenhub/internal/store"
)

// Usage token 用量。
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	Present          bool  `json:"-"`
}

// ToolDelta 流式工具调用片段。
type ToolDelta struct {
	Index int
	ID    string
	Name  string
	Args  string // arguments 增量片段
}

// Delta 一条流式增量。
type Delta struct {
	Role      string // 首条 delta 一般带 role
	Content   string
	Reasoning string
	Tool      *ToolDelta
}

// Finish 结束帧。
type Finish struct {
	Reason string // stop | length | tool_calls | content_filter | ...
	Usage  *Usage
}

// Sink 流式输出汇。提供商解析上游 SSE 后调用；Close 恰好调用一次。
type Sink interface {
	Delta(d Delta)
	Finish(f Finish)
	StreamError(code int64, msg string)
	Close()
}

// Kind 错误分类（决定账号池的冷却/轮换策略）。
type Kind string

const (
	KindQuota   Kind = "quota"   // 额度耗尽
	KindRate    Kind = "rate"    // 限流
	KindAuth    Kind = "auth"    // 凭据失效
	KindModel   Kind = "model"   // 模型不存在/不支持
	KindClient  Kind = "client"  // 请求参数问题（不轮换）
	KindServer  Kind = "server"  // 上游 5xx（短暂冷却后轮换）
	KindCaptcha Kind = "captcha" // 需要验证码（ZCode）
	KindNetwork Kind = "network" // 传输失败
	KindStream  Kind = "stream"  // 流内业务错误
)

// Error 提供商错误。
type Error struct {
	Kind   Kind
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s(%d): %s", e.Kind, e.Status, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Msg)
}

func Err(kind Kind, status int, format string, args ...any) *Error {
	return &Error{Kind: kind, Status: status, Msg: fmt.Sprintf(format, args...)}
}

// ModelInfo 模型。
type ModelInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	MaxInput  int64  `json:"maxInput,omitempty"`
	MaxOutput int64  `json:"maxOutput,omitempty"`
	Credits   string `json:"credits,omitempty"`
}

// Provider 三家上游的统一接口。
type Provider interface {
	Name() string
	// Chat 发送 OpenAI 格式请求并解析上游 SSE 到 sink。
	// 上游始终以 stream=true 调用；非流式由网关用聚合 Sink 完成。
	Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink Sink) error
	// Quota 查询账号额度快照。
	Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error)
	// Checkin 执行签到/领取；不支持时返回 ErrCheckinUnsupported。
	Checkin(ctx context.Context, acct *store.Account) (already bool, msg string, err error)
	SupportsCheckin(acct *store.Account) bool
	// Refresh 刷新 token（无刷新机制时返回 nil）。
	Refresh(ctx context.Context, acct *store.Account) error
	// Models 动态模型列表（需要账号；失败回退 SeedModels）。
	Models(ctx context.Context, acct *store.Account) ([]ModelInfo, error)
	SeedModels() []ModelInfo
}

// ErrCheckinUnsupported 该账号/渠道无签到。
var ErrCheckinUnsupported = fmt.Errorf("checkin not supported")

// LocalSyncer 可选实现：从本机已登录的客户端重新同步凭据。
// 用途：本机导入的账号往往没有 refreshToken（trae 故意不导、zcode 无刷新机制、
// workbuddy 会话文件可能缺失），这类账号点「刷新token」时用它兜底 ——
// 客户端自己会轮换 token，重读本地会话即可拿到最新凭据。
type LocalSyncer interface {
	// SyncFromLocal 把本机客户端当前会话的凭据同步进 acct（按 UID 匹配）。
	// 找不到会话 / 本机登录了别的账号时返回 KindAuth 错误。
	SyncFromLocal(acct *store.Account) error
}

// ── 共享的错误分类 ──

var quotaWords = regexp.MustCompile(`(?i)insufficient|quota|balance|credit|exceed|no available|额度|余额|积分不足|已用完|超出|资源包`)
var authWords = regexp.MustCompile(`(?i)invalid_grant|invalid_token|expired_token|unauthorized|令牌已过期|验证不正确`)
var modelWords = regexp.MustCompile(`(?i)model.*(not found|not exist|不支持|不存在)|not a valid model|unknown model`)

// ClassifyBody 按状态码与响应体分类上游错误。
func ClassifyBody(status int, body []byte) *Error {
	text := string(body)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		trimmed = "(empty body)"
	}
	switch {
	case status == 401 || status == 403:
		return &Error{Kind: KindAuth, Status: status, Msg: cut(trimmed, 300)}
	case status == 429:
		return &Error{Kind: KindRate, Status: status, Msg: cut(trimmed, 300)}
	case status == 402:
		return &Error{Kind: KindQuota, Status: status, Msg: cut(trimmed, 300)}
	case status >= 500:
		return &Error{Kind: KindServer, Status: status, Msg: cut(trimmed, 300)}
	}
	if authWords.MatchString(text) {
		return &Error{Kind: KindAuth, Status: status, Msg: cut(trimmed, 300)}
	}
	if quotaWords.MatchString(text) {
		return &Error{Kind: KindQuota, Status: status, Msg: cut(trimmed, 300)}
	}
	if modelWords.MatchString(text) {
		return &Error{Kind: KindModel, Status: status, Msg: cut(trimmed, 300)}
	}
	if status >= 400 {
		return &Error{Kind: KindClient, Status: status, Msg: cut(trimmed, 300)}
	}
	return &Error{Kind: KindServer, Status: status, Msg: cut(trimmed, 300)}
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
