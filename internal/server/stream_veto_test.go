package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestChatFirstFrameErrorVetoesAndRotates 上游「200 开流 + 第一帧就是 error 帧」：
// 此时一个字节都没写给客户端，网关应否决该号并换下一个——客户端拿到的是另一号的
// 成功流，而不是 200 + error 帧（很多客户端把它当「空成功」，既不重试也不报错）。
func TestChatFirstFrameErrorVetoesAndRotates(t *testing.T) {
	const errStream = "data: {\"error\":{\"message\":\"upstream failed\"}}\n\ndata: [DONE]\n\n"
	const okStream = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
	calls := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls[authz]++
		if authz == "Bearer at-bad" {
			return 200, errStream, true
		}
		return 200, okStream, true
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
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s (want 200：换号后应成功开流)", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"ok"`) {
		t.Errorf("客户端应拿到第二个号的成功流，得到 %s", body)
	}
	if strings.Contains(body, "upstream failed") {
		t.Errorf("被否决的首帧不得透传给客户端: %s", body)
	}
	if calls["Bearer at-bad"] != 1 || calls["Bearer at-good"] != 1 {
		t.Errorf("calls=%v want bad 1 次、good 1 次（首帧否决后换号）", calls)
	}
}

// TestChatFirstFrameErrorAllAccountsCarriesUpstreamText 全部账号都首帧报错：
// 客户端必须拿到**上游原文**（含可排查信息），而不是笼统的 no_healthy_account。
func TestChatFirstFrameErrorAllAccountsCarriesUpstreamText(t *testing.T) {
	const errStream = "data: {\"error\":{\"message\":\"upstream failed\"}}\n\ndata: [DONE]\n\n"
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, errStream, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))

	if rec.Code == http.StatusOK {
		t.Fatalf("首帧 error 且无号可换时不得回 200: %s", rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "upstream failed") {
		t.Errorf("应透传上游原文，得到 %s", body)
	}
	if strings.Contains(body, "no_healthy_account") {
		t.Errorf("上游流级失败不应报成账号耗尽: %s", body)
	}
}

// TestChatFirstFrameContentNotVetoed 首帧是正常内容时不得误否决（回放必须完整）。
func TestChatFirstFrameContentNotVetoed(t *testing.T) {
	const okStream = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\ndata: [DONE]\n\n"
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, okStream, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	// 首帧内容不能因为窥探而丢失。
	if !strings.Contains(body, "hello") || !strings.Contains(body, " world") {
		t.Fatalf("首帧内容被吞掉: %s", body)
	}
}
