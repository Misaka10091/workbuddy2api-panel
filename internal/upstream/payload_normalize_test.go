package upstream

import (
	"encoding/json"
	"testing"
)

// decodeOutboundBody 解出出站请求体（测试辅助）。
func decodeOutboundBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v\n%s", err, raw)
	}
	return obj
}

// TestSplitRealmPrefix realm 前缀协议：只认精确的 cn/global 枚举。
func TestSplitRealmPrefix(t *testing.T) {
	cases := []struct {
		in, realm, bare string
	}{
		{"global:gpt-5.6-sol", "global", "gpt-5.6-sol"},
		{"cn:glm-5.3", "cn", "glm-5.3"},
		{"glm-5.3", "cn", "glm-5.3"},
		{"GLOBAL:x", "cn", "GLOBAL:x"}, // 大小写敏感
		{"us:x", "cn", "us:x"},         // 未知前缀不算前缀
		{"", "cn", ""},
	}
	for _, c := range cases {
		realm, bare := SplitRealmPrefix(c.in)
		if realm != c.realm || bare != c.bare {
			t.Errorf("SplitRealmPrefix(%q)=(%q,%q) want (%q,%q)", c.in, realm, bare, c.realm, c.bare)
		}
	}
}

// TestPrepareBodyStripsRealmPrefix WorkBuddy 上游只认裸模型名：
// 网关侧路由前缀必须在出站前剥掉，否则上游当未知模型。
func TestPrepareBodyStripsRealmPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"global:gpt-5.6-sol", "gpt-5.6-sol"},
		{"cn:glm-5.3", "glm-5.3"},
		{"glm-5.3", "glm-5.3"},
	}
	for _, c := range cases {
		raw, _ := json.Marshal(map[string]any{"model": c.in, "messages": []any{}})
		obj := decodeOutboundBody(t, PrepareBodyOpt(raw, false))
		if got := obj["model"]; got != c.want {
			t.Errorf("model %q 出站应为 %q，得到 %v", c.in, c.want, got)
		}
	}
}

// TestPrepareBodyNormalizeStop 字符串 stop 归一为数组（否则上游 400 code=11101）。
func TestPrepareBodyNormalizeStop(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"model": "glm-5.3", "stop": "\n\n"})
	obj := decodeOutboundBody(t, PrepareBodyOpt(raw, false))
	arr, ok := obj["stop"].([]any)
	if !ok || len(arr) != 1 || arr[0] != "\n\n" {
		t.Fatalf("字符串 stop 应归一为单元素数组，得到 %#v", obj["stop"])
	}

	// 已是数组：原样保留。
	raw2, _ := json.Marshal(map[string]any{"model": "glm-5.3", "stop": []string{"a", "b"}})
	obj2 := decodeOutboundBody(t, PrepareBodyOpt(raw2, false))
	arr2, ok := obj2["stop"].([]any)
	if !ok || len(arr2) != 2 {
		t.Fatalf("数组 stop 不应被改写，得到 %#v", obj2["stop"])
	}
}

// TestPrepareBodyDropsReasoningParts 剔除 reasoning_text / reasoning 片段：
// 上游 content 类型白名单不含它们，命中即 400 code=11101。
func TestPrepareBodyDropsReasoningParts(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"model": "glm-5.3",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "reasoning_text", "text": "思考中"},
					map[string]any{"type": "text", "text": "答案"},
				},
			},
		},
	})
	obj := decodeOutboundBody(t, PrepareBodyOpt(raw, false))
	msgs := obj["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("应保留 1 条消息，得到 %d", len(msgs))
	}
	parts := msgs[0].(map[string]any)["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("reasoning 片段应被剔除，剩余 %d 段", len(parts))
	}
	if got := parts[0].(map[string]any)["type"]; got != "text" {
		t.Errorf("保留的应是 text 片段，得到 %v", got)
	}
}

// TestPrepareBodyDropReasoningKeepsToolCalls 只剩思考片段但带 tool_calls 的消息
// 必须保留（否则破坏工具调用配对，上游返 11148）。
func TestPrepareBodyDropReasoningKeepsToolCalls(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"model": "glm-5.3",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "reasoning", "text": "思考"},
				},
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "read_file",
							"arguments": "{}",
						},
					},
				},
			},
		},
	})
	obj := decodeOutboundBody(t, PrepareBodyOpt(raw, false))
	msgs := obj["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("带 tool_calls 的消息不得被删除，得到 %d 条", len(msgs))
	}
	if parts, ok := msgs[0].(map[string]any)["content"].([]any); !ok || len(parts) != 0 {
		t.Errorf("content 应被清空为空数组，得到 %#v", msgs[0].(map[string]any)["content"])
	}
}
