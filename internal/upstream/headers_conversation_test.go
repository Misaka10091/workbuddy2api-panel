package upstream

import (
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// chatHeaderSet 用给定账号与 meta 跑一次 ChatHeaders，返回捕获到的请求头。
func chatHeaderSet(t *testing.T, a *auth.Auth, meta ChatMeta) map[string]string {
	t.Helper()
	c := New()
	req := httptest.NewRequest("POST", "https://example.invalid/v2/chat/completions", nil)
	c.ChatHeaders(req, a, "", meta)
	out := map[string]string{}
	for k := range req.Header {
		out[k] = req.Header.Get(k)
	}
	return out
}

// TestChatHeadersAlwaysSendConversationTrio 三件套必须恒发：
// 缺任一头上游回 400 code=11-128「conversation id missing」。
func TestChatHeadersAlwaysSendConversationTrio(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	// 零值 meta：夜猫子对话/面板任务这类无 body 会话概念的调用方。
	h := chatHeaderSet(t, a, ChatMeta{})

	for _, name := range []string{"X-Conversation-Id", "X-Conversation-Request-Id", "X-Conversation-Message-Id"} {
		if h[name] == "" {
			t.Errorf("%s 必须恒发，零值 meta 下为空", name)
		}
	}
	if h["X-Conversation-Id"] == h["X-Conversation-Request-Id"] {
		t.Error("会话 ID 与请求 ID 不应相等（官方两族不同）")
	}
	// 聚合主键与消息级 ID 各有其值。
	if h["X-Request-Id"] != h["X-Conversation-Message-Id"] {
		t.Error("X-Request-ID 应等于 X-Conversation-Message-ID")
	}
	if h["X-Root-Request-Id"] != h["X-Conversation-Request-Id"] {
		t.Error("X-Root-Request-ID 应等于 X-Conversation-Request-ID")
	}
}

// TestChatHeadersClientConversationIDWins 客户端声明的会话值必须原样透传（不伪造、不改写）。
func TestChatHeadersClientConversationIDWins(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	h := chatHeaderSet(t, a, ChatMeta{ConversationID: "client-conv-42"})
	if h["X-Conversation-Id"] != "client-conv-42" {
		t.Fatalf("X-Conversation-ID 应透传客户端值，得到 %q", h["X-Conversation-Id"])
	}
}

// TestChatHeadersCacheKeyIDFillsConversation 客户端没声明会话但网关有粘性会话键时，
// 用与 prompt_cache_key 同源的隔离键填充（含账号段，跨账号不共用缓存槽）。
func TestChatHeadersCacheKeyIDFillsConversation(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	h := chatHeaderSet(t, a, ChatMeta{CacheKeyID: "sess-abc"})
	want := buildCacheKey("u1", "sess-abc")
	if h["X-Conversation-Id"] != want {
		t.Fatalf("X-Conversation-ID=%q，期望按会话键派生 %q", h["X-Conversation-Id"], want)
	}
}

// TestChatHeadersTraceAndSpanIndependent X-Trace-ID 不得回落 request id，
// SpanId 不得由 messageID 切片得到（那会带出时间位、与官方形态不符）。
func TestChatHeadersTraceAndSpanIndependent(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	h := chatHeaderSet(t, a, ChatMeta{})

	trace := h["X-Trace-Id"]
	if trace == "" || len(trace) != 32 {
		t.Fatalf("X-Trace-ID 应为 32 hex，得到 %q", trace)
	}
	if trace == h["X-Conversation-Request-Id"] {
		t.Error("X-Trace-ID 不得回落 conversationRequestID（官方两族恒不相等）")
	}
	span := h["X-B3-Spanid"]
	if len(span) != 16 {
		t.Fatalf("X-B3-SpanId 应为 16 hex，得到 %q", span)
	}
	if span == h["X-Conversation-Message-Id"][:16] {
		t.Error("SpanId 不得取 messageID[:16]（会带出时间位）")
	}
	if h["X-B3-Parentspanid"] == "" {
		t.Error("X-B3-ParentSpanId 必须注入（官方每条 chat 都带）")
	}
	if got, want := h["B3"], trace+"-"+span+"-1-"+h["X-B3-Parentspanid"]; got != want {
		t.Errorf("b3 单头=%q，期望 %q", got, want)
	}
	if got := h["Traceparent"]; len(got) != len("00-")+32+1+16+len("-01") {
		t.Errorf("traceparent 形态不对：%q", got)
	}
	if h["Acp-Connection-Id"] == "" {
		t.Error("acp-connection-id 属于标准出站头族，必须注入")
	}
}

// TestChatHeadersClientSDKFamily x-stainless-*/x-agent-* 属官方 chat 出站常驻头族。
func TestChatHeadersClientSDKFamily(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	h := chatHeaderSet(t, a, ChatMeta{})
	for _, name := range []string{
		"X-Agent-Type", "X-Agent-Intent",
		"X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-Os",
		"X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
		"X-Stainless-Retry-Count",
	} {
		if h[name] == "" {
			t.Errorf("%s 必须注入（官方 chat 出站恒发）", name)
		}
	}
}
