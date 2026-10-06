// Package pool 多账号池：模型路由、轮换、冷却策略、按需刷新 token。
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// Pool 账号池。
type Pool struct {
	cfg        *config.Config
	store      *store.Store
	mu         sync.RWMutex
	providers  map[string]provider.Provider
	rr         map[string]int    // provider → 轮换游标
	failStreak map[string]int    // 账号 → 连续失败次数（内存级，重启归零；成功即清零）
}

func New(cfg *config.Config, st *store.Store) *Pool {
	return &Pool{cfg: cfg, store: st, providers: map[string]provider.Provider{}, rr: map[string]int{}, failStreak: map[string]int{}}
}

// Register 注册提供商。
func (p *Pool) Register(prov provider.Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providers[prov.Name()] = prov
}

// Provider 取指定提供商（可返回 nil）。
func (p *Pool) Provider(name string) provider.Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.providers[name]
}

// Providers 全部已注册提供商。
func (p *Pool) Providers() map[string]provider.Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]provider.Provider, len(p.providers))
	for k, v := range p.providers {
		out[k] = v
	}
	return out
}

// shortAlias 模型前缀别名。
var shortAlias = map[string]string{"wb": store.PWorkBuddy, "codebuddy": store.PWorkBuddy, "tr": store.PTrae, "zc": store.PZCode, "glm": store.PZCode}

// ResolveModel 解析 "provider:model" 前缀 / ModelRoutes / 默认提供商。
// 兼容客户端自定义 slug 前缀（如 "custom-local:zcode:glm-5.3-flash"）：
// 从左逐段认领已知提供商/别名，全部未知时剥掉前缀只留最后一段作为裸模型名。
func (p *Pool) ResolveModel(model string) (provider.Provider, string) {
	if strings.Contains(model, ":") {
		segs := strings.Split(model, ":")
		for i := 0; i < len(segs)-1; i++ {
			name := strings.ToLower(segs[i])
			if pv := p.Provider(name); pv != nil {
				return pv, strings.Join(segs[i+1:], ":")
			}
			if real, ok := shortAlias[name]; ok && p.Provider(real) != nil {
				return p.Provider(real), strings.Join(segs[i+1:], ":")
			}
		}
	}
	if name, ok := p.cfg.ModelRoutes[model]; ok && p.Provider(name) != nil {
		return p.Provider(name), model
	}
	def := p.cfg.DefaultProvider
	if p.Provider(def) == nil {
		for _, name := range config.AllProviders() {
			if pc := p.cfg.PC(name); pc.Enabled && p.Provider(name) != nil {
				def = name
				break
			}
		}
	}
	bare := model
	if segs := strings.Split(model, ":"); len(segs) > 1 {
		bare = segs[len(segs)-1]
	}
	return p.Provider(def), bare
}

// candidate 可用账号（enabled、未停用、未冷却）。
// 已知额度快照耗尽（Remain<=0 且 Total>0）的账号排到最后而非直接剔除——
// 轮换时先试有余量的，全都没余量时仍按原顺序兜底，避免白白返回 503。
func (p *Pool) candidates(name string, now time.Time) []*store.Account {
	var ok, exhausted []*store.Account
	for _, a := range p.store.ByProvider(name) {
		if !a.Enabled || a.Dead {
			continue
		}
		if a.CooldownUntil > now.Unix() {
			continue
		}
		if q := a.LastQuota; q != nil && q.Total > 0 && q.Remain <= 0 {
			exhausted = append(exhausted, a)
			continue
		}
		ok = append(ok, a)
	}
	return append(ok, exhausted...)
}

// UnavailableReason 解释某提供商为何无可用账号（面板与 503 消息用）。
func (p *Pool) UnavailableReason(name string) string {
	now := time.Now().Unix()
	all := p.store.ByProvider(name)
	if len(all) == 0 {
		return "没有 " + name + " 账号，请在面板添加"
	}
	dead, cooling, disabled := 0, 0, 0
	for _, a := range all {
		switch {
		case !a.Enabled:
			disabled++
		case a.Dead:
			dead++
		case a.CooldownUntil > now:
			cooling++
		}
	}
	var parts []string
	if disabled > 0 {
		parts = append(parts, "已停用 x"+itoa(disabled))
	}
	if dead > 0 {
		parts = append(parts, "凭据失效 x"+itoa(dead))
	}
	if cooling > 0 {
		parts = append(parts, "冷却中 x"+itoa(cooling))
	}
	return strings.Join(parts, "，")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// trackingSink 记录是否已有内容写出（写出后无法透明轮换）。
type trackingSink struct {
	inner   provider.Sink
	written bool
}

func (t *trackingSink) Delta(d provider.Delta) {
	if d.Content != "" || d.Reasoning != "" || d.Tool != nil {
		t.written = true
	}
	t.inner.Delta(d)
}
func (t *trackingSink) Finish(f provider.Finish)           { t.inner.Finish(f) }
func (t *trackingSink) StreamError(c int64, m string)      { t.inner.StreamError(c, m) }
func (t *trackingSink) Close()                             { t.inner.Close() }

// RefreshAccount 刷新账号凭据，统一入口（面板按钮 / 定时任务 / 请求时按需刷新共用）。
// 策略：有 refreshToken 先走上游刷新；没有、或刷新失败（可能 refreshToken 已被
// 客户端轮换作废）时，回退到「从本机客户端重新同步凭据」——客户端自己会轮换
// token，重读本地会话即可拿到最新值（与 CreditDaddy 的续期思路一致）。
func (p *Pool) RefreshAccount(ctx context.Context, acct *store.Account) error {
	prov := p.Provider(acct.Provider)
	if prov == nil {
		return provider.Err(provider.KindServer, 0, "提供商不可用")
	}
	var rerr error
	if acct.RefreshToken != "" {
		rerr = prov.Refresh(ctx, acct)
		if rerr == nil {
			p.clearCooldown(acct)
			return nil
		}
		logx.Warnf(prov.Name(), "账号 %s 上游刷新失败，尝试本机同步: %v", acct.DisplayedName(), rerr)
	}
	if ls, ok := prov.(provider.LocalSyncer); ok {
		if serr := ls.SyncFromLocal(acct); serr == nil {
			logx.Infof(prov.Name(), "账号 %s 已从本机客户端重新同步凭据", acct.DisplayedName())
			return nil
		} else if rerr == nil {
			rerr = serr
		}
	}
	if rerr == nil {
		rerr = provider.Err(provider.KindAuth, 0, "无 refreshToken 且本机客户端无该账号会话，需要重新登录")
	}
	return rerr
}

// Chat 按池策略执行一次对话请求（内部最多尝试 MaxRotate 个账号）。
func (p *Pool) Chat(ctx context.Context, model string, openaiReq []byte, sink provider.Sink) error {
	prov, bare := p.ResolveModel(model)
	if prov == nil {
		return provider.Err(provider.KindServer, 503, "没有可用的提供商（检查配置中 providers 开关）")
	}
	// 上游不认识 "provider:model" / 客户端 slug 前缀 → 改写为裸模型名
	if bare != "" && bare != model {
		var m map[string]any
		if err := json.Unmarshal(openaiReq, &m); err == nil && m != nil {
			if _, ok := m["model"]; ok {
				m["model"] = bare
				if b2, err := json.Marshal(m); err == nil {
					openaiReq = b2
				}
			}
		}
	}
	ts := &trackingSink{inner: sink}
	cands := p.candidates(prov.Name(), time.Now())
	if len(cands) == 0 {
		return provider.Err(provider.KindQuota, 503, "没有可用的 %s 账号：%s", prov.Name(), p.UnavailableReason(prov.Name()))
	}
	maxTries := p.cfg.MaxRotate
	if maxTries > len(cands)*2 {
		maxTries = len(cands) * 2
	}
	tried := map[string]bool{}
	refreshed := map[string]bool{}
	var lastErr *provider.Error
	attempts := 0
	for attempts < maxTries {
		idx := p.rr[prov.Name()] % len(cands)
		p.rr[prov.Name()] = (p.rr[prov.Name()] + 1) % max(1, len(cands))
		acct := cands[idx]
		if tried[acct.ID] {
			// 一圈试完仍全失败
			if len(tried) >= len(cands) {
				break
			}
			continue
		}
		tried[acct.ID] = true
		attempts++
		// token 将过期 → 先刷新（refreshToken 优先，缺失/失效时回退本机客户端同步）
		if acct.NeedsRefresh(time.Duration(p.cfg.RefreshSkewMin) * time.Minute) {
			if err := p.RefreshAccount(ctx, acct); err != nil {
				logx.Warnf(prov.Name(), "账号 %s 刷新失败: %v", acct.DisplayedName(), err)
				p.markAuthFail(acct, err)
				_ = p.store.Save()
				lastErr = asPerr(err)
				continue
			}
			refreshed[acct.ID] = true
			_ = p.store.Upsert(acct)
		}
		err := prov.Chat(ctx, acct, openaiReq, ts)
		if err == nil {
			p.mu.Lock()
			p.failStreak[acct.ID] = 0
			p.mu.Unlock()
			p.clearCooldown(acct)
			return nil
		}
		pe := asPerr(err)
		lastErr = pe
		logx.Warnf(prov.Name(), "账号 %s 请求失败(%s): %s", acct.DisplayedName(), pe.Kind, pe.Msg)
		p.mu.Lock()
		p.failStreak[acct.ID]++
		streak := p.failStreak[acct.ID]
		p.mu.Unlock()
		if ts.written {
			// 已向客户端写出内容，不能换号重试
			p.applyPolicy(ctx, acct, prov, pe, streak)
			_ = p.store.Save()
			return pe
		}
		// 客户端错误（参数问题）不轮换
		if pe.Kind == provider.KindClient || pe.Kind == provider.KindModel {
			_ = p.store.Save()
			return pe
		}
		// 凭据失效且未刷新过 → 刷新一次再试同号（refreshToken 优先，回退本机同步）
		if pe.Kind == provider.KindAuth && !refreshed[acct.ID] {
			refreshed[acct.ID] = true
			if rerr := p.RefreshAccount(ctx, acct); rerr == nil {
				_ = p.store.Upsert(acct)
				err2 := prov.Chat(ctx, acct, openaiReq, ts)
				if err2 == nil {
					p.clearCooldown(acct)
					return nil
				}
				pe = asPerr(err2)
				lastErr = pe
				if ts.written {
					p.applyPolicy(ctx, acct, prov, pe, streak)
					_ = p.store.Save()
					return pe
				}
			}
		}
		p.applyPolicy(ctx, acct, prov, pe, streak)
		_ = p.store.Save()
		if pe.Kind == provider.KindStream {
			return pe
		}
	}
	if lastErr == nil {
		lastErr = provider.Err(provider.KindServer, 502, "全部账号尝试失败")
	}
	return lastErr
}

// cooldown 时长
var cooldownFor = map[provider.Kind]time.Duration{
	provider.KindQuota:   6 * time.Hour,
	provider.KindRate:    5 * time.Minute,
	provider.KindServer:  30 * time.Second,
	provider.KindNetwork: 15 * time.Second,
	provider.KindCaptcha: 10 * time.Minute,
	provider.KindAuth:    time.Hour,
	provider.KindStream:  0,
	provider.KindModel:   0,
	provider.KindClient:  0,
}

// applyPolicy 按错误类型决定冷却时长；叠加连续失败退避：
// 基础时长 × 2^(min(连击-1,4))（最高 16 倍），防止坏账号被每轮请求反复撞。
func (p *Pool) applyPolicy(ctx context.Context, acct *store.Account, prov provider.Provider, pe *provider.Error, streak int) {
	d, ok := cooldownFor[pe.Kind]
	if !ok || d == 0 {
		return
	}
	if streak > 1 {
		mult := 1 << min(streak-1, 4) // 1,2,4,8,16
		d *= time.Duration(mult)
	}
	if pe.Kind == provider.KindAuth {
		// 有 refreshToken 的账号可恢复；没有则判死
		if acct.RefreshToken == "" {
			acct.Dead = true
			acct.DeadReason = pe.Msg
		}
	}
	if pe.Kind == provider.KindQuota && prov != nil {
		// 主动探测真实余额：仍有余量说明多半是“该模型不在套餐内”或瞬时拒绝，
		// 只做短冷却（避免试一个模型把账号误伤冷却 6 小时）；探不到按短冷却处理。
		pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		snap, err := prov.Quota(pctx, acct)
		cancel()
		if err != nil || snap == nil {
			acct.CooldownUntil = time.Now().Add(5 * time.Minute).Unix()
			acct.CooldownReason = "额度被拒且探测失败，短冷却: " + pe.Msg
			return
		}
		if snap.Remain > 0 {
			acct.CooldownUntil = time.Now().Add(2 * time.Minute).Unix()
			acct.CooldownReason = fmt.Sprintf("额度被拒但仍有余量 %.0f（可能是该模型不在套餐内）: %s", snap.Remain, pe.Msg)
			return
		}
	}
	acct.CooldownUntil = time.Now().Add(d).Unix()
	acct.CooldownReason = string(pe.Kind) + ": " + pe.Msg
}

func (p *Pool) markAuthFail(acct *store.Account, err error) {
	if asPerr(err).Kind == provider.KindAuth && acct.RefreshToken == "" {
		acct.Dead = true
		acct.DeadReason = asPerr(err).Msg
	}
}

func (p *Pool) clearCooldown(acct *store.Account) {
	acct.CooldownUntil = 0
	acct.CooldownReason = ""
	p.mu.Lock()
	p.failStreak[acct.ID] = 0
	p.mu.Unlock()
}

func asPerr(err error) *provider.Error {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe
	}
	return provider.Err(provider.KindNetwork, 0, "%v", err)
}

// ── 状态输出 ──

type AccountStatus struct {
	ID             string               `json:"id"`
	Provider       string               `json:"provider"`
	Realm          string               `json:"realm,omitempty"`
	Nickname       string               `json:"nickname"`
	UID            string               `json:"uid,omitempty"`
	Enabled        bool                 `json:"enabled"`
	Dead           bool                 `json:"dead"`
	DeadReason     string               `json:"deadReason,omitempty"`
	CooldownUntil  int64                `json:"cooldownUntil,omitempty"`
	CooldownReason string               `json:"cooldownReason,omitempty"`
	ExpiresAt      int64                `json:"expiresAt,omitempty"`
	LastCheckinDay string               `json:"lastCheckinDay,omitempty"`
	LastCheckinMsg string               `json:"lastCheckinMsg,omitempty"`
	LastCheckinAt  int64                `json:"lastCheckinAt,omitempty"`
	Quota          *store.QuotaSnapshot `json:"quota,omitempty"`
	HasRefresh     bool                 `json:"hasRefresh"` // false = 只能靠本机客户端同步续期
}

// Status 全部账号状态。
func (p *Pool) Status() []AccountStatus {
	now := time.Now().Unix()
	var out []AccountStatus
	for _, a := range p.store.All() {
		st := AccountStatus{
			ID: a.ID, Provider: a.Provider, Realm: a.Realm, Nickname: a.DisplayedName(), UID: a.UID,
			Enabled: a.Enabled, Dead: a.Dead, DeadReason: a.DeadReason,
			CooldownUntil: a.CooldownUntil, CooldownReason: a.CooldownReason,
			ExpiresAt: a.ExpiresAt,
			LastCheckinDay: a.LastCheckinDay, LastCheckinMsg: a.LastCheckinMsg, LastCheckinAt: a.LastCheckinAt,
			Quota: a.LastQuota,
		}
		if a.CooldownUntil <= now {
			st.CooldownUntil = 0
		}
		st.HasRefresh = a.RefreshToken != ""
		out = append(out, st)
	}
	return out
}

// AllModels 聚合所有启用提供商的模型列表（动态优先，静态兜底）。
func (p *Pool) AllModels(ctx context.Context) map[string][]provider.ModelInfo {
	out := map[string][]provider.ModelInfo{}
	for name, prov := range p.Providers() {
		if pc := p.cfg.PC(name); !pc.Enabled {
			continue
		}
		var list []provider.ModelInfo
		for _, a := range p.candidates(name, time.Now()) {
			if l, err := prov.Models(ctx, a); err == nil && len(l) > 0 {
				list = l
				break
			}
		}
		if list == nil {
			list = prov.SeedModels()
		}
		out[name] = list
	}
	return out
}
