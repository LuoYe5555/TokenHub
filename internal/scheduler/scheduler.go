// Package scheduler 自动领取与维护：每日签到（WorkBuddy CN / Trae）、ZCode 活动领取、
// token 提前刷新、额度快照定期刷新。
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/logx"
	"tokenhub/internal/pool"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
)

// providerP 调度器需要的最小提供商接口。
type providerP interface {
	Checkin(ctx context.Context, acct *store.Account) (already bool, msg string, err error)
	Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error)
	Refresh(ctx context.Context, acct *store.Account) error
	SupportsCheckin(acct *store.Account) bool
}

type Scheduler struct {
	cfg   *config.Config
	store *store.Store
	pool  *pool.Pool

	mu             sync.Mutex
	lastZcodeClaim time.Time
	quotaBusy      bool
	lastKick       time.Time // KickQuotaRefresh 去抖
}

func New(cfg *config.Config, st *store.Store, pl *pool.Pool) *Scheduler {
	return &Scheduler{cfg: cfg, store: st, pool: pl}
}

// Start 启动后台循环。
func (s *Scheduler) Start(ctx context.Context) {
	// 启动 5 秒后先跑一轮（当日已签的账号会被自然日去重跳过）
	go func() {
		time.Sleep(5 * time.Second)
		s.RunCheckinPass(ctx, false)
	}()
	go func() {
		d := s.cfg.QuotaRefresh()
		if d <= 0 {
			return // 用户关闭了额度定时刷新
		}
		tick := time.NewTicker(d)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.RunQuotaRefresh(ctx)
			}
		}
	}()
	go func() {
		tick := time.NewTicker(1 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.RunTokenRefresh(ctx)
				s.RunDeadRevive(ctx)
				s.RunCheckinPass(ctx, false)
			}
		}
	}()
}

func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// RunCheckinPass 签到/领取一轮。force=true 忽略去重（面板手动触发）。
func (s *Scheduler) RunCheckinPass(ctx context.Context, force bool) {
	today := dayKey(time.Now())
	for _, acct := range s.store.All() {
		if ctx.Err() != nil {
			return
		}
		if !acct.Enabled || acct.Dead {
			continue
		}
		prov := s.pool.Provider(acct.Provider)
		if prov == nil {
			continue
		}
		p, ok := prov.(providerP)
		if !ok || !p.SupportsCheckin(acct) {
			continue
		}
		if acct.Provider == store.PZCode {
			s.mu.Lock()
			due := s.lastZcodeClaim.IsZero() ||
				time.Since(s.lastZcodeClaim) >= time.Duration(s.cfg.ZCodeClaimIntervalMin)*time.Minute
			s.mu.Unlock()
			if !due && !force {
				continue
			}
			s.mu.Lock()
			s.lastZcodeClaim = time.Now()
			s.mu.Unlock()
		} else if !force {
			// 签到类：当天成功过（含“已签到”）就跳过；失败过则允许重试
			if acct.LastCheckinDay == today && acct.LastCheckinMsg != "" && !strings.HasPrefix(acct.LastCheckinMsg, "失败") {
				continue
			}
		}
		already, msg, err := p.Checkin(ctx, acct)
		// Checkin 内部可能补了 deviceId（trae 9004 根因），无论成败都落库
		if did := acct.ExtraGet(store.ExtraTraeDeviceID); did != "" {
			_ = s.store.Mutate(acct.ID, func(a *store.Account) { a.ExtraSet(store.ExtraTraeDeviceID, did) })
		}
		if err != nil {
			logx.Warnf(string(acct.Provider), "账号 %s 签到/领取失败: %v", acct.DisplayedName(), err)
			_ = s.store.Mutate(acct.ID, func(a *store.Account) {
				a.LastCheckinAt = time.Now().Unix()
				a.LastCheckinMsg = "失败: " + err.Error()
			})
			continue
		}
		_ = already
		_ = s.store.Mutate(acct.ID, func(a *store.Account) {
			a.LastCheckinAt = time.Now().Unix()
			a.LastCheckinMsg = msg
			if acct.Provider != store.PZCode {
				a.LastCheckinDay = today
			}
		})
		logx.Infof(string(acct.Provider), "账号 %s: %s", acct.DisplayedName(), msg)
		// 领取后刷新额度快照（顺带让有余量的冷却账号解冻判断有据可依）
		if snap, err := p.Quota(ctx, acct); err == nil {
			_ = s.store.Mutate(acct.ID, func(a *store.Account) { a.LastQuota = snap })
		}
	}
}

// RunTokenRefresh 把临期 token 刷新（refreshToken 优先，缺失时回退本机客户端同步）。
func (s *Scheduler) RunTokenRefresh(ctx context.Context) {
	for _, acct := range s.store.All() {
		if ctx.Err() != nil {
			return
		}
		if !acct.Enabled || acct.Dead {
			continue
		}
		// 无 refreshToken 且提供商不支持本机同步 → 没有可刷新途径，跳过
		if acct.RefreshToken == "" {
			if prov := s.pool.Provider(acct.Provider); prov == nil {
				continue
			} else if _, ok := prov.(provider.LocalSyncer); !ok {
				continue
			}
		}
		skew := time.Duration(s.cfg.RefreshSkewMin) * time.Minute
		if !acct.NeedsRefresh(skew) {
			continue
		}
		if err := s.pool.RefreshAccount(ctx, acct); err != nil {
			logx.Warnf(string(acct.Provider), "账号 %s 刷新失败: %v", acct.DisplayedName(), err)
			continue
		}
		_ = s.store.Upsert(acct)
	}
}

// RunDeadRevive 尝试复活误判死亡的账号：凭据失效（Dead）多半是 refreshToken 被客户端
// 轮换作废——本机同步可能已能拿到新凭据，每小时试一次，成功即解除死亡标记。
func (s *Scheduler) RunDeadRevive(ctx context.Context) {
	for _, acct := range s.store.All() {
		if ctx.Err() != nil {
			return
		}
		if !acct.Enabled || !acct.Dead {
			continue
		}
		prov := s.pool.Provider(acct.Provider)
		if prov == nil {
			continue
		}
		_, hasSync := prov.(provider.LocalSyncer)
		if acct.RefreshToken == "" && !hasSync {
			continue // 没有任何刷新途径，复活无望
		}
		if err := s.pool.RefreshAccount(ctx, acct); err != nil {
			continue // 仍失败，下小时再试
		}
		s.store.Mutate(acct.ID, func(a *store.Account) {
			a.Dead = false
			a.DeadReason = ""
			a.CooldownUntil = 0
			a.CooldownReason = ""
		})
		logx.Infof(string(acct.Provider), "账号 %s 已自动复活（凭据刷新成功）", acct.DisplayedName())
	}
}

// RunQuotaRefresh 刷新全部账号额度快照（并发 3）。
func (s *Scheduler) RunQuotaRefresh(ctx context.Context) {
	s.mu.Lock()
	if s.quotaBusy {
		s.mu.Unlock()
		return
	}
	s.quotaBusy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.quotaBusy = false
		s.mu.Unlock()
	}()
	s.refreshAll(ctx)
}

// RefreshOne 拉取单个账号的额度快照并落库（供网关调用后自动同步用量）。
func (s *Scheduler) RefreshOne(acctID string) {
	acct := s.store.Get(acctID)
	if acct == nil || !acct.Enabled || acct.Dead {
		return
	}
	prov := s.pool.Provider(acct.Provider)
	p, ok := prov.(providerP)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	snap, err := p.Quota(ctx, acct)
	if err != nil {
		return
	}
	_ = s.store.Mutate(acctID, func(x *store.Account) { x.LastQuota = snap })
	s.rejoinIfRecovered(acct, snap)
	s.autoSwitchIfExhausted(acct, snap)
}

// KickQuotaRefresh 异步触发一轮全量额度刷新（5 秒去抖 + 进行中跳过）。
func (s *Scheduler) KickQuotaRefresh() {
	s.mu.Lock()
	if s.quotaBusy || time.Since(s.lastKick) < 5*time.Second {
		s.mu.Unlock()
		return
	}
	s.lastKick = time.Now()
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.RunQuotaRefresh(ctx)
	}()
}

func (s *Scheduler) refreshAll(ctx context.Context) {
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, acct := range s.store.All() {
		if ctx.Err() != nil || !acct.Enabled || acct.Dead {
			continue
		}
		prov := s.pool.Provider(acct.Provider)
		if prov == nil {
			continue
		}
		p, ok := prov.(providerP)
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(a *store.Account, pp providerP) {
			defer wg.Done()
			defer func() { <-sem }()
			snap, err := pp.Quota(ctx, a)
			if err != nil {
				return
			}
			_ = s.store.Mutate(a.ID, func(x *store.Account) { x.LastQuota = snap })
			s.rejoinIfRecovered(a, snap)
			s.autoSwitchIfExhausted(a, snap)
		}(acct, p)
	}
	wg.Wait()
}

// autoSwitchIfExhausted 定时刷新发现 Trae 积分耗尽 → 提前冷却该账号，
// 池子自然跳到下一个号（相当于自动换号，状态落盘 accounts.json）。
// 仅在总余量明确 >0 且剩余 <=0 时触发，避免查询异常误伤；下一轮刷新若
// 积分恢复（签到/新周期），冷却到期后账号自动回归。
func (s *Scheduler) autoSwitchIfExhausted(acct *store.Account, snap *store.QuotaSnapshot) {
	if !s.cfg.TraeAutoSwitch || acct.Provider != store.PTrae {
		return
	}
	if snap == nil || snap.Total <= 0 || snap.Remain > 0 {
		return
	}
	if acct.CooldownUntil > time.Now().Unix() {
		return // 已在冷却中
	}
	until := time.Now().Add(6 * time.Hour)
	_ = s.store.Mutate(acct.ID, func(a *store.Account) {
		a.CooldownUntil = until.Unix()
		a.CooldownReason = fmt.Sprintf("积分耗尽（余 %.0f/%.0f），已自动切换到其他账号；检测到积分恢复后自动回归", snap.Remain, snap.Total)
	})
	logx.Infof(store.PTrae, "账号 %s 积分耗尽，自动换号（冷却至 %s）",
		acct.DisplayedName(), until.Format("15:04"))
}

// rejoinIfRecovered 因「积分耗尽」冷却的 Trae 账号检测到积分恢复 → 立即解除冷却，
// 无缝回归账号池，不再干等冷却到期。
// 会话/凭据在冷却期间原样保留：token 由 RunTokenRefresh 照常续期（含无
// refreshToken 的本机同步账号），额度快照照常刷新，回归即可直接服务。
func (s *Scheduler) rejoinIfRecovered(acct *store.Account, snap *store.QuotaSnapshot) {
	if !s.cfg.TraeAutoSwitch || acct.Provider != store.PTrae {
		return
	}
	if snap == nil || snap.Remain <= 0 {
		return
	}
	if acct.CooldownUntil <= time.Now().Unix() {
		return // 未在冷却中
	}
	if !strings.Contains(acct.CooldownReason, "积分耗尽") {
		return // 其他原因（限流/额度拒绝等）的冷却不在此解除，按原策略到期
	}
	_ = s.store.Mutate(acct.ID, func(a *store.Account) {
		a.CooldownUntil = 0
		a.CooldownReason = ""
	})
	logx.Infof(store.PTrae, "账号 %s 积分已恢复（余 %.0f/%.0f），解除冷却自动回归",
		acct.DisplayedName(), snap.Remain, snap.Total)
}
