// fingerprint_risk_test.go 出站指纹风控护栏（issue #51）：chat 头族形态、桌面端
// 上报头与设备指纹。这些断言是「上游看到什么」的契约，改协议前先看这里。
package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func sseResponse() *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
}

// TestChatHeadersStripBrowserSemantics 桌面端 chat 不发浏览器语义头，也不发
// X-No-* 哨兵头；Accept 只 application/json；X-Private-Data 恒发；X-Product 恒 SaaS。
func TestChatHeadersStripBrowserSemantics(t *testing.T) {
	var got http.Header
	c := testClient(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Clone()
		return sseResponse(), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	rc, status, _, err := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{})
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	rc.Close()

	for _, k := range []string{
		"Origin", "Referer", "Accept-Language", "X-Machine-ID", "X-Session-ID",
		"X-No-Enterprise-Id", "X-No-Department-Info", "X-Refresh-Token",
	} {
		if v := got.Get(k); v != "" {
			t.Errorf("chat 不应发 %s=%q（官方桌面端不发）", k, v)
		}
	}
	if v := got.Get("Accept"); v != "application/json" {
		t.Errorf("Accept=%q want application/json", v)
	}
	if v := got.Get("X-Private-Data"); v != "true" {
		t.Errorf("X-Private-Data=%q want true", v)
	}
	if v := got.Get("X-Product"); v != "SaaS" {
		t.Errorf("X-Product=%q want SaaS（产品名由 X-IDE-* 承载）", v)
	}
	if v := got.Get("X-IDE-Name"); v != "WorkBuddy" {
		t.Errorf("X-IDE-Name=%q want WorkBuddy", v)
	}
	if v := got.Get("Authorization"); v != "Bearer at" {
		t.Errorf("Authorization=%q", v)
	}
}

// TestDesktopFingerprintCNDeviceFields 国内版桌面上报指纹必须带 cpuModel / qimei36，
// 且 osVersion / cpuCores / memorySize 用真机值——缺 cpuModel 是最明显的非桌面端特征。
func TestDesktopFingerprintCNDeviceFields(t *testing.T) {
	fp := desktopFingerprint(&auth.Auth{UID: "u1"})
	if fp["cpuModel"] == nil || fp["cpuModel"] == "" {
		t.Error("缺 cpuModel：上游据此判定非桌面端")
	}
	if fp["qimei36"] == nil || fp["qimei36"] == "" {
		t.Error("国内版必须带 qimei36")
	}
	if fp["osVersion"] != "10.0.28000" || fp["cpuCores"] != 8 || fp["memorySize"] != 16 {
		t.Errorf("设备指纹非真机值: osVersion=%v cpuCores=%v memorySize=%v",
			fp["osVersion"], fp["cpuCores"], fp["memorySize"])
	}
	// 幂等：同一账号每次派生同值（固定设备，不随重启抖动）。
	if again := desktopFingerprint(&auth.Auth{UID: "u1"}); again["qimei36"] != fp["qimei36"] {
		t.Error("qimei36 应稳定派生（同号恒同值）")
	}
}

// TestReportDesktopEventHeadShape X-Domain 发裸主机名、X-Request-ID 为 32 hex 消息 ID。
func TestReportDesktopEventHeadShape(t *testing.T) {
	var got http.Header
	c := testClient(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Clone()
		return jsonResp(200, `{"code":0}`), nil
	})
	if err := c.ReportDesktopEvent(&auth.Auth{AccessToken: "at", UID: "u1"}, DesktopEvent{"eventCode": "x"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if v := got.Get("X-Domain"); v != "chat.example" {
		t.Errorf("X-Domain=%q want chat.example（裸主机名，不是 URL）", v)
	}
	id := got.Get("X-Request-ID")
	if len(id) != 32 || strings.ContainsAny(id, "-./") || !isLowerHex(id) {
		t.Errorf("X-Request-ID=%q want 32 hex", id)
	}
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}
