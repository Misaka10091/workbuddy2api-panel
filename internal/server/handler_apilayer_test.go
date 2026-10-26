package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatQueueFullFailsFastWithRetryAfter 6021 队列满：渠道级容量信号，
// 不罚号不轮转，503 + Retry-After 透传原文。
func TestChatQueueFullFailsFastWithRetryAfter(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
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

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s (want 503 渠道容量)", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("渠道排队必须带 Retry-After（客户端据此退避）")
	}
	if !strings.Contains(rec.Body.String(), "6021") {
		t.Errorf("应透传上游原文（含 code/requestId）: %s", rec.Body)
	}
	// 不罚号：不冷却、不禁用、不记熔断。
	for _, uid := range []string{"bad", "good"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled || st.BreakerFails != 0 {
			t.Errorf("账号 %s 不应被罚: %+v", uid, st)
		}
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
