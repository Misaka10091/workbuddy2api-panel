package upstream

import (
	"encoding/json"
	"testing"
)

// TestInjectConversationFields 轮级字段只写「当前轮」的最后一条 user 消息，
// 并与头里的 X-Conversation-Request-ID 一致（头体一致，避免可判别形态）。
func TestInjectConversationFields(t *testing.T) {
	const rid = "01a160b2c3d4e5f60718293a4b5c6d7e"
	raw, _ := json.Marshal(map[string]any{
		"model": "glm-5.3",
		"messages": []any{
			map[string]any{"role": "user", "content": "第一轮"},
			map[string]any{"role": "assistant", "content": "答"},
			map[string]any{"role": "user", "content": "当前轮"},
		},
	})
	out := decodeOutboundBody(t, injectConversationFields(raw, rid))
	msgs := out["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("消息条数不应变化，得到 %d", len(msgs))
	}
	// 最后一条 user：带上轮级 ID + 默认 agent + startsNewUserRequest。
	cur := msgs[2].(map[string]any)
	if cur["conversationRequestId"] != rid {
		t.Fatalf("当前轮应写 conversationRequestId=%q，得到 %v", rid, cur["conversationRequestId"])
	}
	if cur["startsNewUserRequest"] != true {
		t.Errorf("startsNewUserRequest 应为 true")
	}
	if cur["agent"] != agentCLI {
		t.Errorf("agent 应补 %q，得到 %v", agentCLI, cur["agent"])
	}
	// 历史消息不得被编造轮级 ID（客户端没给，网关无从得知）。
	hist := msgs[0].(map[string]any)
	if _, present := hist["conversationRequestId"]; present {
		t.Errorf("历史消息不得补 conversationRequestId: %v", hist["conversationRequestId"])
	}
}

// TestInjectConversationFieldsPreservesClientValue 客户端已自带时原值优先，不覆盖。
func TestInjectConversationFieldsPreservesClientValue(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "x", "conversationRequestId": "client-rid"},
		},
	})
	out := decodeOutboundBody(t, injectConversationFields(raw, "server-rid"))
	msgs := out["messages"].([]any)
	if got := msgs[0].(map[string]any)["conversationRequestId"]; got != "client-rid" {
		t.Fatalf("客户端自带值应保留，得到 %v", got)
	}
}

// TestInjectConversationFieldsNoop 无轮级 ID / 无 user 消息 / 坏 body 时原样返回。
func TestInjectConversationFieldsNoop(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
	})
	if got := injectConversationFields(raw, ""); string(got) != string(raw) {
		t.Fatal("convReqID 为空时应原样返回")
	}

	assistantOnly, _ := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "assistant", "content": "x"}},
	})
	if got := injectConversationFields(assistantOnly, "rid"); string(got) != string(assistantOnly) {
		t.Fatal("没有 user 消息时不应改写")
	}

	if got := injectConversationFields([]byte("{not json"), "rid"); string(got) != "{not json" {
		t.Fatal("坏 body 应原样返回（不二次错误化）")
	}
}
