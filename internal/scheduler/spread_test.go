// spread_test.go 账号摊开与夜猫子上限护栏（issue #45 同口径）。
package scheduler

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// TestAccountSpreadDeterministic 同一天、同一批账号 → 同一份计划（重启不重排）。
func TestAccountSpreadDeterministic(t *testing.T) {
	setJitterSeed(42)
	defer setJitterSeed(0)
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	uids := []string{"a", "b", "c", "d"}

	p1 := newAccountSpread(taskCheckin, uids, 10*time.Minute, 30*time.Second, now)
	p2 := newAccountSpread(taskCheckin, uids, 10*time.Minute, 30*time.Second, now)
	if p1 == nil || p2 == nil {
		t.Fatal("plan should not be nil")
	}
	for _, uid := range uids {
		if !p1.at[uid].Equal(p2.at[uid]) {
			t.Errorf("uid=%s 计划不稳定: %v vs %v", uid, p1.at[uid], p2.at[uid])
		}
	}
}

// TestAccountSpreadMinGap 按执行顺序相邻账号至少隔 minGap（冲突时把靠后的推开）。
func TestAccountSpreadMinGap(t *testing.T) {
	setJitterSeed(7)
	defer setJitterSeed(0)
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	list := []pool.Status{{UID: "a"}, {UID: "b"}, {UID: "c"}, {UID: "d"}, {UID: "e"}}
	gap := 30 * time.Second
	p := newAccountSpread(taskActivity, []string{"a", "b", "c", "d", "e"}, 10*time.Minute, gap, now)

	ordered := p.order(list)
	for i := 1; i < len(ordered); i++ {
		prev, cur := p.at[ordered[i-1].UID], p.at[ordered[i].UID]
		if d := cur.Sub(prev); d < gap {
			t.Errorf("第 %d 个账号间隔 %v < %v", i, d, gap)
		}
	}
}

// TestNightCapListSize 单轮参与数受上限约束；上限 >= 账号数时原样返回。
func TestNightCapListSize(t *testing.T) {
	list := []pool.Status{{UID: "a"}, {UID: "b"}, {UID: "c"}, {UID: "d"}}
	now := time.Date(2026, 10, 26, 23, 0, 0, 0, time.UTC)
	if got := nightCapList(list, 2, now); len(got) != 2 {
		t.Errorf("nightCapList=%d want 2", len(got))
	}
	if got := nightCapList(list, 10, now); len(got) != 4 {
		t.Errorf("上限大于账号数时应原样返回，得到 %d", len(got))
	}
	if got := nightCapList(list, 0, now); len(got) != 4 {
		t.Errorf("0 = 不限制，得到 %d", len(got))
	}
}

// TestPeriodicJitterBounded 抖动量不超过 frac*interval。
func TestPeriodicJitterBounded(t *testing.T) {
	setJitterSeed(3)
	defer setJitterSeed(0)
	interval := 5 * time.Minute
	d := periodicJitter(interval, 0.2, "balance")
	if d > interval/5 || d < -interval/5 {
		t.Errorf("jitter=%v 超出 ±20%%", d)
	}
	if periodicJitter(interval, 0, "balance") != 0 {
		t.Error("frac=0 应关闭抖动")
	}
}
