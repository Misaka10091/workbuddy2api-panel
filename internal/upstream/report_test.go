package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestReportChatActivitySendsArrayWithUserID 断言出站 body 是数组、含 userId、eventCode 正确。
//
// 通道反转（2026-10-28，上游 ee1c80d）：/v2/report 从 billingBase 改走 **chatBase**，
// 故测试 Client 现在要配 ChatBaseCN（配 BillingBaseCN 已经打不到这个端点了）。
func TestReportChatActivitySendsArrayWithUserID(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			t.Errorf("path=%s want /v2/report", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method=%s want POST", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		var arr []map[string]any
		if err := json.Unmarshal(raw, &arr); err != nil {
			t.Fatalf("body must be a JSON array: %v (body=%s)", err, raw)
		}
		got = arr
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	if err := c.ReportChatActivity(&auth.Auth{AccessToken: "at", UID: "u-active"}, "wb2api-123", ""); err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d want 1", len(got))
	}
	ev := got[0]
	if ev["eventCode"] != "chat_request_send" {
		t.Errorf("eventCode=%v want chat_request_send", ev["eventCode"])
	}
	if ev["userId"] != "u-active" {
		t.Errorf("userId=%v want u-active（缺失则服务端 200 但静默丢弃）", ev["userId"])
	}
	if ev["conversationId"] != "wb2api-123" {
		t.Errorf("conversationId=%v want wb2api-123", ev["conversationId"])
	}
	if ev["mode"] != "craft" {
		t.Errorf("mode=%v want craft", ev["mode"])
	}
	// 出站 body 必须是数组（以 [ 开头），不是单个对象。
	//
	// 5.9.3 字段集增删（PRD R1-10，上游 ee1c80d）——这是「字段集对齐」唯一的回归点：
	// 新增维度必须在、删掉的键必须不在，否则多一个客户端不发的键同样是指纹。
	if ev["isMaxMode"] != true || ev["source"] != "work" {
		t.Errorf("isMaxMode/source=%v/%v want true/work", ev["isMaxMode"], ev["source"])
	}
	if ev["agentName"] != "cli" || ev["agentType"] != "main" {
		t.Errorf("agentName/agentType=%v/%v want cli/main（旧值 default/conversation）", ev["agentName"], ev["agentType"])
	}
	if ev["vcsType"] != "unknown" {
		t.Errorf("vcsType=%v want unknown", ev["vcsType"])
	}
	if _, ok := ev["inputAttemptId"]; !ok {
		t.Error("inputAttemptId 缺失（5.9.3 新增维度）")
	}
	// requestID 为空时回落 conversationID，故两键同值。
	if ev["codebuddy.session_id"] != "wb2api-123" || ev["codebuddy.conversation_request_id"] != "wb2api-123" {
		t.Errorf("codebuddy.* 两键必须与 conversationId/requestId 同值：%v/%v",
			ev["codebuddy.session_id"], ev["codebuddy.conversation_request_id"])
	}
	for _, gone := range []string{
		"maxToken", "maxSteps", "temperature", "maxRetries",
		"skillCount", "totalCount", "fileUri", "expertId",
	} {
		if _, ok := ev[gone]; ok {
			t.Errorf("5.9.3 真机不存在的字段仍然出站: %s", gone)
		}
	}
	// 桌面指纹打底：userId / timestamp / presentAt / 设备三元组由 desktopFingerprint 提供
	// （chatRequestEvent 结构不再重复持有这几个字段，靠这层 merge 保证不丢）。
	for _, need := range []string{"userId", "timestamp", "presentAt", "reportDelay", "machineId", "sessionId", "ideVersion"} {
		if _, ok := ev[need]; !ok {
			t.Errorf("桌面指纹字段缺失: %s", need)
		}
	}
}

// TestReportChatActivityServerError 业务 code 非 0 / HTTP 5xx 返回 *Error。
func TestReportChatActivityServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	err := c.ReportChatActivity(&auth.Auth{AccessToken: "at", UID: "u1"}, "cid", "")
	if err == nil {
		t.Fatal("want error on 500")
	}
}
