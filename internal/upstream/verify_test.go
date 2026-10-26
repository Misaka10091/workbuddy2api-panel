package upstream

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestVerifySessionByRefresh 网关 401 的求证判据：
//   - refresh 成功 → alive（401 只是瞬时拦截）
//   - refresh 被应用层明确拒绝（4xx 业务）→ dead
//   - 网络/5xx/429/refresh 端点自己也被拦 → inconclusive（既不能判活也不能判死）
func TestVerifySessionByRefresh(t *testing.T) {
	cases := []struct {
		name string
		resp *http.Response
		err  error
		want VerifyVerdict
	}{
		{
			name: "refresh ok",
			resp: jsonResp(200, `{"code":0,"data":{"accessToken":"newat","refreshToken":"newrt","expiresIn":3600}}`),
			want: VerifyAlive,
		},
		{
			name: "refresh app-rejected 400",
			resp: jsonResp(400, `{"code":12153,"msg":"Invalid refresh token"}`),
			want: VerifyDead,
		},
		{
			name: "refresh 5xx",
			resp: jsonResp(502, `bad gateway`),
			want: VerifyInconclusive,
		},
		{
			name: "refresh 429",
			resp: jsonResp(429, `{"code":10001,"msg":"请求处理中"}`),
			want: VerifyInconclusive,
		},
		{
			name: "refresh endpoint itself blocked by gateway 401 page",
			resp: &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       newBody(`<html><head><title>401 Authorization Required</title></head></html>`),
			},
			want: VerifyInconclusive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, verr := tc.resp, tc.err
			c := testClient(func(r *http.Request) (*http.Response, error) { return resp, verr })
			a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
			if got := c.VerifySessionByRefresh(a); got != tc.want {
				t.Fatalf("verdict=%v want %v", got, tc.want)
			}
		})
	}
}

// TestVerifySessionByRefreshNoRT 无 refresh token 时没有判词（不臆造死号）。
func TestVerifySessionByRefreshNoRT(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		t.Fatal("不应发起 refresh 请求")
		return nil, nil
	})
	a := &auth.Auth{AccessToken: "at"}
	if got := c.VerifySessionByRefresh(a); got != VerifyInconclusive {
		t.Fatalf("verdict=%v want inconclusive", got)
	}
}

// TestSessionDeadButUsable refresh 判死但 AT 仍可用 → 账号此刻能用，不该禁用。
func TestSessionDeadButUsable(t *testing.T) {
	fresh := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}
	if !SessionDeadButUsable(fresh) {
		t.Error("AT 未过期时应报告 usable")
	}
	expired := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	if SessionDeadButUsable(expired) {
		t.Error("AT 已过期时不得报告 usable")
	}
	if SessionDeadButUsable(nil) {
		t.Error("nil 账号不得报告 usable")
	}
}

// newBody 构造一个可读的响应体（避免依赖调用方包内已有辅助的签名差异）。
func newBody(s string) *closingReader { return &closingReader{Reader: strings.NewReader(s)} }

type closingReader struct{ *strings.Reader }

func (c *closingReader) Close() error { return nil }
