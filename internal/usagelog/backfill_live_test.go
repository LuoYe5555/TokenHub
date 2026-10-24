package usagelog

import (
	"context"
	"os"
	"testing"
	"time"

	"tokenhub/internal/pricing"
)

// TestBackfillLive 对真实数据目录执行一次历史花费回填（环境开关门控）。
func TestBackfillLive(t *testing.T) {
	if os.Getenv("UL_BACKFILL_LIVE") == "" {
		t.Skip("需要 UL_BACKFILL_LIVE=1")
	}
	dir := os.Getenv("UL_DATA_DIR")
	if dir == "" {
		t.Skip("需要 UL_DATA_DIR")
	}
	pt := pricing.Load(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := pt.Refresh(ctx); err != nil {
		t.Logf("联网刷新失败（用内置价回填）: %v", err)
	}
	l := Load(dir)
	l.SetPricer(pt)
	l.Backfill()
	td := l.Today()
	t.Logf("今日: tokens=%d reqs=%d cost=%.2f CNY", td.Tokens, td.Reqs, td.Cost)
	tk, rq, cost := l.AllTime()
	t.Logf("全历史: tokens=%d reqs=%d cost=%.2f CNY", tk, rq, cost)
	st := l.Stats(-1)
	t.Logf("全部范围统计: total=%d cost=%.2f, 模型数=%d", st.Total, st.Cost, len(st.Models))
	for _, m := range st.Models {
		t.Logf("  %s: tokens=%d cost=%.2f", m.Model, m.Tokens, m.Cost)
	}
}
