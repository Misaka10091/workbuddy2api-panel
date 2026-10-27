// allowed_budget_test.go 网关级模型白名单与积分闸（止血）护栏。
package server

import (
	"strings"
	"testing"
	"time"
)

func TestAllowedInRules(t *testing.T) {
	allow := []string{"deepseek-v4.1-flash", "cn:glm-5.2", "global:gpt-6-sol"}
	cases := []struct {
		realm, model string
		want         bool
	}{
		{"cn", "deepseek-v4.1-flash", true},     // 裸名两域可见
		{"global", "deepseek-v4.1-flash", true}, // 同上
		{"cn", "glm-5.2", true},                 // cn 专属
		{"global", "glm-5.2", false},            // 不可跨域
		{"global", "gpt-6-sol", true},
		{"cn", "gpt-6-sol", false},
		{"cn", "unknown-model", false},
	}
	for _, c := range cases {
		if got := allowedIn(allow, c.realm, c.model); got != c.want {
			t.Errorf("allowedIn(%q,%q)=%v want %v", c.realm, c.model, got, c.want)
		}
	}
	// 空名单 = 不启用（放行）。
	if !allowedIn(nil, "cn", "anything") {
		t.Error("空名单应放行")
	}
}

func TestModelAllowedFromHandler(t *testing.T) {
	h := &Handler{cfg: Config{AllowedModels: []string{"cn:glm-5.2"}}}
	if !h.modelAllowed("cn", "glm-5.2") {
		t.Error("cn:glm-5.2 应放行")
	}
	if h.modelAllowed("global", "glm-5.2") {
		t.Error("global 不应被 cn: 条目放行")
	}
	if !(&Handler{}).modelAllowed("cn", "x") {
		t.Error("未配置白名单时应放行")
	}
}

func TestBudgetDailyAndBurstGuards(t *testing.T) {
	b := newDailyBudget(10)
	b.setGuards(1, 0)

	if ok, _ := b.admit(); !ok {
		t.Fatal("初始应放行")
	}
	// 未观测到的扣费不计账（缺失≠0）。
	b.add(5, false)
	if ok, _ := b.admit(); !ok {
		t.Fatal("未观测扣费不应计入")
	}
	b.add(0.6, true)
	b.add(0.6, true) // 5 分钟窗累计 1.2 ≥ 1 → 突发闸拒绝
	ok, msg := b.admit()
	if ok || !strings.Contains(msg, "burst") {
		t.Errorf("突发闸未生效: ok=%v msg=%q", ok, msg)
	}

	// 当日上限：新实例只配 daily。
	d := newDailyBudget(1)
	d.add(1.5, true)
	if ok, msg := d.admit(); ok || msg != "" {
		t.Errorf("当日上限应拒绝且无附加文案: ok=%v msg=%q", ok, msg)
	}
}

func TestBudgetCacheLowRateGuard(t *testing.T) {
	b := newDailyBudget(0)
	b.setGuards(0, 50) // 命中率低于 50% 连续 5 轮
	for i := 0; i < budgetCacheStreak; i++ {
		b.observeCache(0, 100, true)
	}
	ok, msg := b.admit()
	if ok || !strings.Contains(msg, "cache") {
		t.Errorf("缓存率闸未生效: ok=%v msg=%q", ok, msg)
	}
	// 上游没给缓存字段（has=false）不参与判定。
	c := newDailyBudget(0)
	c.setGuards(0, 50)
	for i := 0; i < budgetCacheStreak; i++ {
		c.observeCache(0, 100, false)
	}
	if ok, _ := c.admit(); !ok {
		t.Error("无缓存观测时不应触发缓存闸")
	}
}

func TestCSTDayRollsAtMidnight(t *testing.T) {
	// 23:59 与 00:01（CST）必须属于不同自然日。
	a := time.Date(2026, 10, 26, 15, 59, 0, 0, time.UTC) // 23:59 CST
	b := time.Date(2026, 10, 26, 16, 1, 0, 0, time.UTC)  // 00:01 CST 次日
	if cstDay(a) == cstDay(b) {
		t.Errorf("CST 自然日未跨天: %s", cstDay(a))
	}
}
