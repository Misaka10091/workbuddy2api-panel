package upstream

import (
	"compress/gzip"
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

// TestChatBodyGzipThreshold chat 出站 body 按阈值 gzip：只有 ≥ 阈值才压，且**只在压缩
// 成功时**设置 Content-Encoding；阈值调到极大等价于关闭压缩（设计裁决：GzipMinChatBody
// 是 Client 字段而不是配置项/包级常量，就是为了让 wire 断言能这样关掉它）。
//
// 来源：5.9.3 真机 body 以 `1f 8b` 开头、带 `Content-Encoding: gzip`（上游 47f1ed6）。
// 反向保护：压缩不得改变 body 语义，小 body 必须保持明文（否则所有既有 wire 断言都得
// 先解压，测试维护面直接翻倍）。
func TestChatBodyGzipThreshold(t *testing.T) {
	big := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"` +
		strings.Repeat("x", 2000) + `"}]}`)
	small := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)

	for _, tc := range []struct {
		name     string
		body     []byte
		minBody  int // 0 = 用内置默认阈值
		wantGzip bool
	}{
		{"small_body_plain", small, 0, false},
		{"large_body_gzip", big, 0, true},
		{"threshold_disabled_equivalently", big, 1 << 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotEncoding string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotEncoding = r.Header.Get("Content-Encoding")
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer srv.Close()

			c := New()
			c.HTTP = srv.Client()
			c.ChatHTTP = srv.Client()
			c.ChatBaseCN = srv.URL
			c.GzipMinChatBody = tc.minBody

			rc, status, respBody, err := c.ChatStreamContext(context.Background(),
				&auth.Auth{UID: "u-1", AccessToken: "at"}, tc.body, "", ChatMeta{})
			if rc != nil {
				_ = rc.Close()
			}
			if err != nil || status >= 400 {
				t.Fatalf("chat: status=%d err=%v body=%s", status, err, respBody)
			}

			payload := gotBody
			if tc.wantGzip {
				if gotEncoding != "gzip" {
					t.Fatalf("Content-Encoding=%q want gzip（body 超阈值）", gotEncoding)
				}
				if len(gotBody) < 2 || gotBody[0] != 0x1f || gotBody[1] != 0x8b {
					t.Fatalf("body 不是 gzip 流（前两字节 %x）", gotBody[:min(2, len(gotBody))])
				}
				zr, zerr := gzip.NewReader(strings.NewReader(string(gotBody)))
				if zerr != nil {
					t.Fatalf("gunzip: %v", zerr)
				}
				defer zr.Close()
				if payload, zerr = io.ReadAll(zr); zerr != nil {
					t.Fatalf("read gunzip: %v", zerr)
				}
			} else if gotEncoding != "" {
				t.Fatalf("Content-Encoding=%q want 空（不该压缩）", gotEncoding)
			}

			var obj map[string]any
			if err := json.Unmarshal(payload, &obj); err != nil {
				t.Fatalf("出站 body 不是 JSON: %v", err)
			}
			if obj["stream"] != true {
				t.Error("压缩不该改变 body 语义（stream 应仍为 true）")
			}
		})
	}
}
