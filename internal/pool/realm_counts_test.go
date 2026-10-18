package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// realmCountsPool 构造 cn/global 混池，global 开关打开（生产缺省）。
func realmCountsPool(t *testing.T) *Pool {
	t.Helper()
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn2", Domain: ""})
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.Add(&auth.Auth{UID: "g2", Domain: "workbuddy.ai"})
	return p
}

// TestCountsByRealmSplitsDomains 两域各自计数，互不串味。
func TestCountsByRealmSplitsDomains(t *testing.T) {
	p := realmCountsPool(t)

	rc := p.CountsByRealm()
	if len(rc) != 2 {
		t.Fatalf("CountsByRealm 返回 %d 个域, want 2（cn/global 恒在）", len(rc))
	}
	if got := rc["cn"]; got.Realm != "cn" || got.Total != 2 || got.Healthy != 2 {
		t.Errorf("cn counts=%+v want realm=cn total=2 healthy=2", got)
	}
	if got := rc["global"]; got.Realm != "global" || got.Total != 2 || got.Healthy != 2 {
		t.Errorf("global counts=%+v want realm=global total=2 healthy=2", got)
	}
	if !rc["cn"].Servable || !rc["global"].Servable {
		t.Errorf("两域都有健康账号，Servable 应为 true: cn=%v global=%v",
			rc["cn"].Servable, rc["global"].Servable)
	}
}

// TestCountsByRealmSeparatesDisabledAndPaused 禁用与暂停分开计：
// 这是面板「禁用 N / 暂停 M」分开展示的前提（CountsDetailedForRealm 把两者合并，拆不开）。
func TestCountsByRealmSeparatesDisabledAndPaused(t *testing.T) {
	p := realmCountsPool(t)
	p.Disable("cn1", "test disable")
	p.Pause("cn2")
	p.Pause("g1")

	rc := p.CountsByRealm()
	if c := rc["cn"]; c.Disabled != 1 || c.Paused != 1 || c.Healthy != 0 {
		t.Errorf("cn=%+v want disabled=1 paused=1 healthy=0", c)
	}
	if g := rc["global"]; g.Disabled != 0 || g.Paused != 1 || g.Healthy != 1 {
		t.Errorf("global=%+v want disabled=0 paused=1 healthy=1", g)
	}
	// 与既有合并口径的差异就是本方法存在的理由：CountsDetailedForRealm 把 paused
	// 并进 disabled（cn 域 2 个不可选），这里必须仍是 1+1。
	_, _, _, mergedDisabled, _ := p.CountsDetailedForRealm("cn")
	if mergedDisabled != 2 {
		t.Fatalf("CountsDetailedForRealm(cn) disabled=%d want 2（合并口径不应被改动）", mergedDisabled)
	}
}

// TestCountsByRealmCreditsPerDomain 积分按域分别累加，混池时也能看出各域余量。
func TestCountsByRealmCreditsPerDomain(t *testing.T) {
	p := realmCountsPool(t)
	now := time.Now()
	p.SetCreditsDetailed("cn1", 300, 500, 0, time.Time{}, 0)
	p.SetCreditsDetailed("cn2", 200, 500, 0, time.Time{}, 0)
	p.SetCreditsDetailed("g1", 42, 100, 0, time.Time{}, 0)

	rc := p.CountsByRealm()
	if c := rc["cn"]; c.Credits != 500 || c.CreditsTotal != 1000 || !c.CreditsKnown {
		t.Errorf("cn credits=%d total=%d known=%v want 500/1000/true", c.Credits, c.CreditsTotal, c.CreditsKnown)
	}
	if g := rc["global"]; g.Credits != 42 || g.CreditsTotal != 100 || !g.CreditsKnown {
		t.Errorf("global credits=%d total=%d want 42/100", g.Credits, g.CreditsTotal)
	}
	// g2 从未查过余额 → 该域 CreditsKnown 仍为 true（g1 已知），但总额只累加已知账号，
	// 不会因为 g2 的零值把 100 算成 200。
	if rc["global"].CreditsTotal == 200 {
		t.Errorf("global total 把未查询账号的零值也累加了：%d", rc["global"].CreditsTotal)
	}
	_ = now
}

// TestCountsByRealmEmptyDomainsAlwaysPresent 没有账号的域也返回零值结构：
// 面板直接渲染两张卡片，不需要判空分支（纯 global 部署也看得见「国内版 0」）。
func TestCountsByRealmEmptyDomainsAlwaysPresent(t *testing.T) {
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	p := New("")
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})

	rc := p.CountsByRealm()
	cn, ok := rc["cn"]
	if !ok {
		t.Fatal("cn 域缺失：面板两张卡片会缺一张")
	}
	if cn.Total != 0 || cn.Healthy != 0 || cn.Servable {
		t.Errorf("cn=%+v want 全零（该域无账号）", cn)
	}
	if rc["global"].Total != 1 {
		t.Errorf("global total=%d want 1", rc["global"].Total)
	}
}

// TestCountsByRealmFollowsGlobalEscapeHatch 逃生门（global.enabled=false）下
// global 账号被 Realm() 降级为 cn —— 域计数必须与选号/路由同源，不能各算一套。
func TestCountsByRealmFollowsGlobalEscapeHatch(t *testing.T) {
	p := realmCountsPool(t)
	auth.SetGlobalEnabled(false)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	rc := p.CountsByRealm()
	if rc["cn"].Total != 4 {
		t.Errorf("逃生门下 cn total=%d want 4（global 号降级并入 cn）", rc["cn"].Total)
	}
	if rc["global"].Total != 0 {
		t.Errorf("逃生门下 global total=%d want 0", rc["global"].Total)
	}
	// CountsDetailedForRealm 是同源判定，两者必须一致（防未来某处各写一套）。
	total, _, _, _, _ := p.CountsDetailedForRealm("cn")
	if total != rc["cn"].Total {
		t.Errorf("CountsDetailedForRealm(cn)=%d 与 CountsByRealm=%d 不一致", total, rc["cn"].Total)
	}
}
