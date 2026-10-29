package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// fastRotation 让轮转退避不真睡：rotateBackoffBase 是包级 var，本包无 t.Parallel
// 用例，逐用例存取即可。退避时长本身由 backoff.go 的界断言覆盖；聊天路径的用例
// 只关心「轮了几个号、最终映射成什么」。
func fastRotation(t *testing.T) {
	t.Helper()
	old := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = old })
}

// TestChatQueueBusyRotatesThen429WithoutPenalty 6020/6021 渠道级排队：**不罚号但换号
// 重试**，轮转耗尽后回 429 + Retry-After，上游原文原样透传。
//
// 本用例反转了原 TestChatQueueFullFailsFastWithRetryAfter 的断言（旧：fail-fast 503、
// good 号 0 次调用）。反转后的语义：上游对 6020/6021 本身回 429（6020 还带
// data.retry_after），而 WorkBuddy CLI 对 503 直接中断任务（2026-10-28 实案：两次 503
// 后任务死亡）、对 429 按 Retry-After 自动重发；排队可用性又是间歇的（同实案 33 秒内
// 同模型恢复），所以恢复请求内轮转、终态透传 429。
// 「零惩罚」一字未改：2026-10-15 整池停摆的成因是「把排队当账号故障罚号」，不是换号，
// 本轮保留零动作分支，成因已切开。
// 原用例**没有断言 calls**，这正是 fail-fast 一直没被发现的原因——新版必须查。
func TestChatQueueBusyRotatesThen429WithoutPenalty(t *testing.T) {
	fastRotation(t)
	calls := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls[authz]++
		return 429, `{"code":6021,"msg":"queue.full.title","requestId":"b1bc87ea3a771e835e388328713d47cf"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	p.SetCredits("bad", 2000, 0)
	p.SetCredits("good", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d body=%s (want 429：轮转耗尽后的可重试排队语义，非 503)", rec.Code, rec.Body)
	}
	// 6021 body 没有 data.retry_after → 本地兜底 5s（口径与旧 fail-fast 分支一致）。
	if ra := rec.Header().Get("Retry-After"); ra != "5" {
		t.Errorf("Retry-After=%q want 5（上游未给等待口径时的本地兜底）", ra)
	}
	if !strings.Contains(rec.Body.String(), "6021") || !strings.Contains(rec.Body.String(), "b1bc87ea") {
		t.Errorf("429 body 应透传上游原文（含 code+requestId）: %s", rec.Body)
	}
	// 轮转证据：两号各一次（tried 保证不重复选号）。
	if calls["Bearer at-bad"] != 1 || calls["Bearer at-good"] != 1 {
		t.Errorf("calls=%v want 两号各一次（请求内轮转，非 fail-fast）", calls)
	}
	// 零惩罚：无冷却、无禁用、无熔断计数、无连败累计。
	for _, uid := range []string{"bad", "good"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled || st.BreakerFails != 0 || st.ErrTotal != 0 {
			t.Errorf("账号 %s 不得因排队被罚: %+v", uid, st)
		}
	}
}

// TestChatQueueWaitingRetryAfterFromBody 6020 排队的等待口径在 body
// （data.retry_after=40），不在响应头：端到端必须把它透成 Retry-After 头，而不是
// 落 5s 兜底。上游真机形态（2026-10-28）连 estimated_wait=12510 一起给，只认
// retry_after（见 parseQueueRetryAfter）。
func TestChatQueueWaitingRetryAfterFromBody(t *testing.T) {
	fastRotation(t)
	const upstreamBody = `{"code":6020,"msg":"queue.waiting.title","requestId":"01a181ca3b67934d01c3f64599c8490c","data":{"queue_position":2502,"queue_size":"999+","retry_after":40,"estimated_wait":12510}}`
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 429, upstreamBody, false
	})
	p := testPoolWith(&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d body=%s want 429", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "40" {
		t.Errorf("Retry-After=%q want 40（body data.retry_after 透传，非本地兜底）", ra)
	}
	// message 不替换：仍是上游原文（含 queue_position / retry_after）。
	if !strings.Contains(rec.Body.String(), "queue.waiting.title") {
		t.Errorf("message 应原样透传上游原文: %s", rec.Body)
	}
	// 排队不罚号：单号池也必须保持健康（下一轮请求照常参与选号）。
	if st, _ := p.Status("q1"); st.Cooling || st.Disabled || st.ErrTotal != 0 {
		t.Errorf("排队不得罚号: %+v", st)
	}
}

// TestChatQueueBusyThenQuotaBlockEmitsNoRetryAfter 排队（6020/6021）与模型级**用量配额**
// （6004）在同一次请求里相遇时，末端描述的是配额，必须仍然遵守「配额分支不发
// Retry-After」这条硬约定：客户端普遍把该头当「睡多久」的指令，而配额窗口内睡完重试
// 一样失败（见 modelBlockAllQuota 处的注释）。
//
// 为什么需要这条用例：retryAfterSec 本轮上移到 `var ue` 之前，与 ErrUpstreamBusy
// （排队 → 429 + 兜底 5s）共用同一个写头出口。若不在模型级覆盖前显式归零，排队兜底的
// 5s 会随着配额响应一起报出去，客户端于是安静地睡 5 秒再撞同一堵配额墙——这正是硬约定
// 要避免的形态。
//
// 构造：bad 号先撞排队，且在该次上游调用期间被后台判死（模拟该号并发失效），于是下一轮
// 选号只剩「该模型 6004 配额冷却」的 good 号，末端走 quota 分支。
func TestChatQueueBusyThenQuotaBlockEmitsNoRetryAfter(t *testing.T) {
	fastRotation(t)
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	// good 号对该模型处于 6004 用量配额冷却（上游权威重置墙钟 2h 后）。
	p.CooldownSoftForModel("good", 10*time.Minute, time.Now().Add(2*time.Hour), "glm-5.2", "6004 model rate limit")
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		// 排队响应回来时 bad 号被后台判死：下一轮选号不再有可用号。
		p.Disable("bad", "concurrent session dead")
		return 429, `{"code":6021,"msg":"queue.full.title","requestId":"b1bc87ea"}`, false
	})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d body=%s want 429（配额语义）", rec.Code, rec.Body)
	}
	// 配额响应用上游业务码形态（6004），不是 upstream_busy。
	if !strings.Contains(rec.Body.String(), "6004") {
		t.Errorf("body 应是 6004 配额形态: %s", rec.Body)
	}
	// 硬约定：配额分支不得发 Retry-After（排队分支的 5s 兜底不许漏进来）。
	if ra := rec.Header().Get("Retry-After"); ra != "" {
		t.Errorf("Retry-After=%q want 空（6004 配额分支不发该头，否则客户端静默挂起）", ra)
	}
}

// TestChatToolMismatchFailsFast 11148 配对断裂：请求级错误，400 直返不轮转。
func TestChatToolMismatchFailsFast(t *testing.T) {
	calls := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls[authz]++
		return 400, `{"code":11148,"msg":"tool calls and tool results do not match"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	p.SetCredits("bad", 2000, 0)
	p.SetCredits("good", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s (want 400)", rec.Code, rec.Body)
	}
	if calls["Bearer at-bad"] != 1 || calls["Bearer at-good"] != 0 {
		t.Errorf("calls=%v want bad 1 次、good 0 次（零轮转）", calls)
	}
	if !strings.Contains(rec.Body.String(), "tool_call_mismatch") {
		t.Errorf("code 应为 tool_call_mismatch: %s", rec.Body)
	}
	st, _ := p.Status("bad")
	if st.Cooling || st.Disabled || st.ErrTotal != 0 {
		t.Errorf("11148 不得罚号: %+v", st)
	}
}

// TestChatModelParamInvalidFailsFast 11133 参数被拒：400 直返不轮转，code 区分于 11101。
func TestChatModelParamInvalidFailsFast(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 400, `{"code":11133,"msg":"model_param_invalid: max_tokens must be >= 16"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s (want 400)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "model_param_invalid") {
		t.Errorf("code 应为 model_param_invalid: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "no_healthy_account") {
		t.Errorf("请求级错误不得报成账号耗尽: %s", rec.Body)
	}
}

// TestChatModelQuotaReturns429WithBusinessCode 6004 用量配额：全池该模型都在额度
// 窗口内 → 429 + 上游数字业务码，且**不发 Retry-After**（几十分钟的冷却原样报出去
// 会让客户端静默挂起）。
//
// 这条同时是回归护栏：pool 里存的锁因是 handler 写的短标签（"6004 model rate limit"），
// 不是上游 JSON 原文——只认 `"code":6004` 的判据会漏判，把配额锁误报成 400「模型不可用」。
func TestChatModelQuotaReturns429WithBusinessCode(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 429, `{"code":6004,"msg":"使用量已超出频率限制，将在 2026-10-26 04:00:00 重置"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at-a1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at-a2", ExpiresAt: 9999999999},
	)
	p.SetCredits("a1", 2000, 0)
	p.SetCredits("a2", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d body=%s (want 429 配额语义)", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("配额锁不得发 Retry-After（会让客户端静默挂起），得到 %q", got)
	}
	if !strings.Contains(rec.Body.String(), "6004") {
		t.Errorf("应透出上游业务码 6004: %s", rec.Body)
	}
	// 账号本身健康（只是该模型额度用满），不得被禁用。
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Disabled {
			t.Errorf("配额锁不得禁用账号 %s: %+v", uid, st)
		}
	}
}

// TestChatModelBusyRetryableWithRetryAfter 14003 模型繁忙：可重试语义，
// 不是「模型不可用」——客户端应等待重试而不是放弃。
func TestChatModelBusyRetryableWithRetryAfter(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 429, `{"code":14003,"msg":"当前模型请求繁忙，请切换模型或稍后重试"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at-a1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at-a2", ExpiresAt: 9999999999},
	)
	p.SetCredits("a1", 2000, 0)
	p.SetCredits("a2", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	// 全池该模型都在繁忙短窗内 → 可重试的 503，不是 400「模型不可用」。
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s (want 503 可重试)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "model_busy") {
		t.Errorf("code 应为 model_busy: %s", rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("模型繁忙应带 Retry-After（按最早解封时刻）")
	}
	// 模型级冷却：账号本身不该被账号级冷却（换模型立即可用）。
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Disabled {
			t.Errorf("账号 %s 不得被禁用: %+v", uid, st)
		}
	}
}

// TestChatChannelCheckSoftCoolsInsteadOfDisable 11-128 渠道校验失败是请求级可重试
// 信号：软冷却，不再永久禁用（否则好号要人工解冻）。
func TestChatChannelCheckSoftCoolsInsteadOfDisable(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 400, `{"code":"11-128","msg":"request illegal: channel verification failed"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code == 0 {
		t.Fatal("响应缺失")
	}
	st, _ := p.Status("u1")
	if st.Disabled {
		t.Fatalf("11-128 不应永久禁用账号: %+v", st)
	}
	if !st.Cooling {
		t.Errorf("11-128 应软冷却（等处自愈）: %+v", st)
	}
}

// TestChatAuthForbiddenStillDisables 11140 是账号级授权风控：仍硬禁用（与 11-128 分野）。
func TestChatAuthForbiddenStillDisables(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 403, `{"code":11140,"msg":"request illegal"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Fatalf("11140 应硬禁用（账号级授权风控）: %+v", st)
	}
}

// TestChatModelForbiddenUsesOwnReason 11158 的负缓存 reason 要与 11102 分开，
// 便于台账读因与权限恢复后的清理。
func TestChatModelForbiddenUsesOwnReason(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 404, `{"code":11158,"msg":"model gpt-5.6-sol is not allowed for user type"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s (want 400 模型不可用)", rec.Code, rec.Body)
	}
	st, _ := p.Status("u1")
	// 11158 是 (账号, 模型) 级负缓存：账号不进账号级冷却/禁用，只锁该模型。
	if st.Disabled {
		t.Errorf("11158 不得禁用账号: %+v", st)
	}
	if len(st.RateLimitedModels) == 0 && !st.Cooling {
		t.Errorf("11158 应写入模型级负缓存: %+v", st)
	}
}
