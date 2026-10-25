package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestOutboundWireForm 端到端钉住发往 WorkBuddy 的真实报文形态：
// 路径、请求头族、请求体归一化结果。一次 ChatStreamContext 走完整出站管线
// （prepareBody + ChatHeaders），断言的是上游**真正收到**的东西。
//
// 这是本仓与上游对齐调用层的回归护栏：任何一处归一化被改坏，这里会直接失败。
func TestOutboundWireForm(t *testing.T) {
	var gotPath string
	var gotHeader http.Header
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New()
	c.HTTP = srv.Client()
	c.ChatHTTP = srv.Client()
	c.ChatBaseCN = srv.URL

	a := &auth.Auth{
		UID:          "u-1",
		AccessToken:  jwtWithClaim("token_source", "enterprise_switch"),
		EnterpriseID: "ent-9",
		ExpiresAt:    1 << 40,
	}

	// 客户端形态：realm 前缀 + 字符串 stop + 首条即 user（无 system）。
	in := []byte(`{"model":"cn:glm-5.3","stop":"\n\n","messages":[{"role":"user","content":"hi"}]}`)
	rc, status, body, err := c.ChatStreamContext(context.Background(), a, in, "", ChatMeta{
		ConversationID:        "conv-42",
		ConversationRequestID: "01a160b2c3d4e5f60718293a4b5c6d7e",
	})
	if rc != nil {
		_ = rc.Close()
	}
	if err != nil {
		t.Fatalf("ChatStreamContext: %v (status=%d body=%s)", err, status, body)
	}

	// 路径：CN 出站固定 /v2/chat/completions。
	if gotPath != "/v2/chat/completions" {
		t.Errorf("path=%q want /v2/chat/completions", gotPath)
	}

	// 会话三件套恒发，且与传入的轮级 ID 一致。
	if gotHeader.Get("X-Conversation-ID") != "conv-42" {
		t.Errorf("X-Conversation-ID=%q", gotHeader.Get("X-Conversation-ID"))
	}
	if gotHeader.Get("X-Conversation-Request-ID") != "01a160b2c3d4e5f60718293a4b5c6d7e" {
		t.Errorf("X-Conversation-Request-ID=%q", gotHeader.Get("X-Conversation-Request-ID"))
	}
	if gotHeader.Get("X-Conversation-Message-ID") == "" || gotHeader.Get("X-Request-ID") == "" {
		t.Error("消息级 ID 头必须注入")
	}
	if gotHeader.Get("X-Trace-ID") == "" || gotHeader.Get("X-B3-TraceId") == "" ||
		gotHeader.Get("X-B3-SpanId") == "" || gotHeader.Get("b3") == "" || gotHeader.Get("traceparent") == "" {
		t.Error("链路头族（trace/B3/b3/traceparent）必须注入")
	}
	// 企业号身份头。
	if gotHeader.Get("X-Auth-Method") != "enterprise_switch" {
		t.Errorf("X-Auth-Method=%q", gotHeader.Get("X-Auth-Method"))
	}
	if gotHeader.Get("X-Userinfo") == "" {
		t.Error("企业号应带 X-Userinfo")
	}
	// SDK 指纹族。
	if gotHeader.Get("X-Stainless-Lang") == "" || gotHeader.Get("X-Agent-Type") == "" {
		t.Error("x-stainless-*/x-agent-* 族必须注入")
	}

	// 请求体：realm 前缀已剥、stream 强制 true、stop 归一为数组、
	// 当前轮 user 消息带轮级字段。
	if gotBody["model"] != "glm-5.3" {
		t.Errorf("出站 model=%v want glm-5.3（已剥 realm 前缀）", gotBody["model"])
	}
	if gotBody["stream"] != true {
		t.Errorf("stream=%v want true", gotBody["stream"])
	}
	stop, ok := gotBody["stop"].([]any)
	if !ok || len(stop) != 1 || stop[0] != "\n\n" {
		t.Errorf("stop 应归一为数组，得到 %#v", gotBody["stop"])
	}
	if _, has := gotBody["prompt_cache_key"]; !has {
		t.Error("应注入 prompt_cache_key（前缀缓存）")
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("messages 不得为空")
	}
	var lastUser map[string]any
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if role, _ := m["role"].(string); strings.EqualFold(role, "user") {
			lastUser = m
		}
	}
	if lastUser == nil {
		t.Fatal("找不到 user 消息")
	}
	if lastUser["conversationRequestId"] != "01a160b2c3d4e5f60718293a4b5c6d7e" {
		t.Errorf("body 侧轮级 ID=%v want 与头一致", lastUser["conversationRequestId"])
	}
	if lastUser["agent"] != agentCLI {
		t.Errorf("agent=%v want %q", lastUser["agent"], agentCLI)
	}
}
