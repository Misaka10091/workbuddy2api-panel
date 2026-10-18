package scheduler

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestNormalizeRealm 域作用域归一化：空/未知一律回落全池（"")。
//
// 为什么未知值不该报错或变空集：域作用域是运维便利开关，拼错一个字母就
// 「一个号都没跑到」比「跑全池」更难发现（静默失效）。
func TestNormalizeRealm(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"cn", "cn"},
		{"global", "global"},
		{"GLOBAL", ""}, // 大小写不匹配 → 回落全池（不做模糊纠正）
		{"all", ""},    // 前端「全部」不上送 realm，这里防外部脚本乱传
		{"cn ", ""},    // 不 trim：调用方（panel）已 trim，这里只做严格匹配
		{"foo", ""},
	}
	for _, c := range cases {
		if got := normalizeRealm(c.in); got != c.want {
			t.Errorf("normalizeRealm(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestInScope 域谓词：空域恒 true；非空域按 auth.Realm() 判定。
func TestInScope(t *testing.T) {
	withGlobalSwitch(t)
	cn := &auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"}
	gl := globalAccount(t, "g1")

	if !inScope(cn, "") || !inScope(gl, "") {
		t.Error("空域作用域应包含所有账号")
	}
	if !inScope(cn, "cn") || inScope(cn, "global") {
		t.Error("cn 账号只应落在 cn 作用域")
	}
	if !inScope(gl, "global") || inScope(gl, "cn") {
		t.Error("global 账号只应落在 global 作用域")
	}
	if inScope(nil, "cn") {
		t.Error("nil 账号不应落在任何非空作用域")
	}
}

// realmScopePool 混池 fixture：cn1/cn2 + g1，global 开关打开。
func realmScopePool(t *testing.T) *pool.Pool {
	t.Helper()
	withGlobalSwitch(t)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn1", AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn2", AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: 9999999999, Domain: ""})
	p.Add(globalAccount(t, "g1"))
	return p
}

// TestRunActivityRealmOnlyTouchesThatRealm 「只对国际版活跃上报」不得碰到国内版账号。
//
// 这是「国内版 / 国际版分开管理」的核心保证：混池时按域批量操作必须真正隔离，
// 否则运维以为只动了国际版，实际把国内版也打了一遍上游（风控风险 + 结果难对账）。
func TestRunActivityRealmOnlyTouchesThatRealm(t *testing.T) {
	fastActivity(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			cnHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer cnSrv.Close()
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			globalHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer glSrv.Close()

	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    glSrv.URL,
		BillingBaseGlobal: glSrv.URL,
	}
	s := New(Config{Pool: realmScopePool(t), Upstream: up})

	s.RunActivityRealm("global")

	if n := globalHits.Load(); n != 1 {
		t.Errorf("global 上报次数=%d want 1", n)
	}
	if n := cnHits.Load(); n != 0 {
		t.Errorf("只对 global 执行，CN base 却被打了 %d 次（域隔离失效）", n)
	}
}

// TestRunActivityRealmEmptyIsWholePool 空域 = 全池（既有行为）：两域都上报。
func TestRunActivityRealmEmptyIsWholePool(t *testing.T) {
	fastActivity(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			cnHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer cnSrv.Close()
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/report" {
			globalHits.Add(1)
		}
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer glSrv.Close()

	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    glSrv.URL,
		BillingBaseGlobal: glSrv.URL,
	}
	s := New(Config{Pool: realmScopePool(t), Upstream: up})

	s.RunActivityRealm("")

	if n := cnHits.Load(); n != 2 {
		t.Errorf("CN 上报次数=%d want 2（cn1+cn2）", n)
	}
	if n := globalHits.Load(); n != 1 {
		t.Errorf("global 上报次数=%d want 1", n)
	}
}

// TestRunKeepaliveRealmOnlyTouchesThatRealm 只保活国际版：CN 账号的 token 不该被刷。
func TestRunKeepaliveRealmOnlyTouchesThatRealm(t *testing.T) {
	withGlobalSwitch(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnHits.Add(1)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer cnSrv.Close()
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalHits.Add(1)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer glSrv.Close()

	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    glSrv.URL,
		BillingBaseGlobal: glSrv.URL,
	}
	s := New(Config{Pool: realmScopePool(t), Upstream: up})

	s.RunKeepaliveRealm("global")

	if n := globalHits.Load(); n == 0 {
		t.Error("global 账号未被保活（global base 零请求）")
	}
	if n := cnHits.Load(); n != 0 {
		t.Errorf("只对 global 保活，CN base 却被打了 %d 次（域隔离失效）", n)
	}
}

// TestRunCheckinRealmScopeStopsStreakCrossRealm 「只对某域签到」不得顺带跑另一域的连登管家。
//
// 背景：RunCheckinNow 末尾会调 RunStreakBonusNow（连登兑换 + 抽奖）。若它不退化为
// 同一域作用域，按域签到就会顺带把另一域的兑换/抽奖也跑一遍——运维没要求动它。
func TestRunCheckinRealmScopeStopsStreakCrossRealm(t *testing.T) {
	withGlobalSwitch(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnHits.Add(1)
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	defer cnSrv.Close()
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalHits.Add(1)
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	defer glSrv.Close()

	// 只放一个 global 账号：D4 门控会跳过它的签到，但必须**不**触碰 CN 端点，
	// 且连登管家也不该因为「遍历了全部账号」而打到 CN base。
	p := pool.New("")
	p.Add(globalAccount(t, "g1"))
	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    glSrv.URL,
		BillingBaseGlobal: glSrv.URL,
	}
	s := New(Config{Pool: p, Upstream: up})

	s.RunCheckinRealm("global")

	if n := cnHits.Load(); n != 0 {
		t.Errorf("对 global 域签到，CN base 却被打了 %d 次（连登管家未跟随域作用域）", n)
	}
}

// TestRunBalanceRefreshRealmOnlyTouchesThatRealm 只刷某域余额：另一域不上游请求。
func TestRunBalanceRefreshRealmOnlyTouchesThatRealm(t *testing.T) {
	withGlobalSwitch(t)
	var cnHits, globalHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnHits.Add(1)
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	defer cnSrv.Close()
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalHits.Add(1)
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	defer glSrv.Close()

	up := &upstream.Client{
		HTTP:              cnSrv.Client(),
		GlobalEnabled:     true,
		ChatBaseCN:        cnSrv.URL,
		BillingBaseCN:     cnSrv.URL,
		ChatBaseGlobal:    glSrv.URL,
		BillingBaseGlobal: glSrv.URL,
	}
	s := New(Config{Pool: realmScopePool(t), Upstream: up})

	s.RunBalanceRefreshRealm("cn")

	if n := cnHits.Load(); n == 0 {
		t.Error("cn 账号余额未刷新（CN base 零请求）")
	}
	if n := globalHits.Load(); n != 0 {
		t.Errorf("只对 cn 刷余额，global base 却被打了 %d 次（域隔离失效）", n)
	}
}
