package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// newDeadRefreshUpstream 构造一个「刷新恒判死、chat 恒成功」的假上游：
// 按路径分流，refresh 端点回 12153 业务信封，其余照常回 SSE 内容。
func newDeadRefreshUpstream() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
				return &http.Response{
					StatusCode: 401,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"code":12153,"msg":"Offline user session not found"}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

// TestChatPreRefreshDeadRTButLiveATKeepsAccount 请求前预刷新判死，但 AT 仍未过期：
// 这号此刻 chat 是通的，不得禁号，也不得换号——用现有 AT 走完这一跳。
//
// 恢复成「refresh 失败即 Disable」后，本测试必须失败。
func TestChatPreRefreshDeadRTButLiveATKeepsAccount(t *testing.T) {
	p := testPoolWith(&auth.Auth{
		UID: "u1", AccessToken: "at1", RefreshToken: "rt1",
		// 落在默认 10m 预刷新窗口内，但此刻仍然有效。
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix(),
	})
	h := NewHandler(Config{Pool: p, Upstream: newDeadRefreshUpstream()})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s（AT 仍有效应照常放行，不得因 RT 失效而禁号）", rec.Code, rec.Body)
	}
	st, _ := p.Status("u1")
	if st.Disabled {
		t.Fatalf("AT 仍有效时预刷新判死不得禁用账号: %+v", st)
	}
	if st.Cooling {
		t.Errorf("容忍一次续期失败不应顺带冷却账号: %+v", st)
	}
}

// TestChatPreRefreshDeadRTAndExpiredATDisables AT 也已过期时，refresh 判死就是真的
// 不可用：保持原有处置（禁用），避免把死号留在池里反复送死。
func TestChatPreRefreshDeadRTAndExpiredATDisables(t *testing.T) {
	p := testPoolWith(&auth.Auth{
		UID: "u1", AccessToken: "at1", RefreshToken: "rt1",
		ExpiresAt: 1, // 早已过期
	})
	h := NewHandler(Config{Pool: p, Upstream: newDeadRefreshUpstream()})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Fatalf("AT 已过期且 RT 判死应禁用账号: %+v", st)
	}
	if st.DisabledReason != "refresh session dead" {
		t.Errorf("disabled_reason=%q want refresh session dead", st.DisabledReason)
	}
	if rec.Code == http.StatusOK {
		t.Errorf("死号不应产出正常回复: %s", rec.Body)
	}
}

// newGateway401Upstream 网关层 401（APISIX/openresty HTML 页、无业务信封）+
// refresh 被应用层明确拒绝（12153）→ VerifySessionByRefresh 判死。
func newGateway401Upstream() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
				return &http.Response{
					StatusCode: 400,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"code":12153,"msg":"Invalid refresh token"}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader(`<html><head><title>401 Authorization Required</title></head></html>`)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

// TestGateway401VerifyDeadNeedsConsecutiveHits 网关 401 经 refresh 求证判死后，
// 仍要**连续 sessionDeadThreshold 次**才禁用：风控窗口里 401 会成串出现，单次求证
// 失败就把号永久踢出，等于让一次边缘风控升级成人工重登。与保活同一口径。
func TestGateway401VerifyDeadNeedsConsecutiveHits(t *testing.T) {
	p := testPoolWith(&auth.Auth{
		UID: "u1", AccessToken: "at1", RefreshToken: "rt1",
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(), // 不触发预刷新
	})
	h := NewHandler(Config{Pool: p, Upstream: newGateway401Upstream()})

	doChat := func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	}

	doChat()
	if st, _ := p.Status("u1"); st.Disabled {
		t.Fatalf("第 1 次网关 401 判死不得直接禁用: %+v", st)
	}
	for i := 0; i < pool.SessionDeadThreshold()-1; i++ {
		doChat()
	}
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Fatalf("连续 %d 次网关 401 判死应禁用: %+v", pool.SessionDeadThreshold(), st)
	}
	if st.DisabledReason != "12153 session dead" {
		t.Errorf("disabled_reason=%q want 12153 session dead", st.DisabledReason)
	}
}
