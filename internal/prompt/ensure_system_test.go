package prompt

import (
	"encoding/json"
	"testing"
)

func decodeMsgs(t *testing.T, body []byte) []any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("出站 body 非法: %v\n%s", err, body)
	}
	msgs, _ := obj["messages"].([]any)
	return msgs
}

// TestEnsureSystemFirstPrepends 首条不是 system 时在最前补一条 system ——
// 上游对首条 user 直接 400 code=11-128「first message is not system prompt」。
func TestEnsureSystemFirstPrepends(t *testing.T) {
	in, _ := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "你好"},
			map[string]any{"role": "assistant", "content": "在"},
		},
	})
	msgs := decodeMsgs(t, EnsureSystemFirst(in, "GATEWAY PROMPT"))
	if len(msgs) != 3 {
		t.Fatalf("应补一条 system，得到 %d 条", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "GATEWAY PROMPT" {
		t.Fatalf("首条应是网关 system，得到 %v", first)
	}
	// 其余消息逐字不动。
	if msgs[1].(map[string]any)["content"] != "你好" {
		t.Error("原有消息不得被改写")
	}
}

// TestEnsureSystemFirstFallbackToDegraded 未配提示词时用中性兜底，绝不因为没配就不补。
func TestEnsureSystemFirstFallbackToDegraded(t *testing.T) {
	in, _ := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
	})
	msgs := decodeMsgs(t, EnsureSystemFirst(in, ""))
	if len(msgs) != 2 {
		t.Fatalf("空 fallback 也应补 system，得到 %d 条", len(msgs))
	}
	if msgs[0].(map[string]any)["content"] != Degraded {
		t.Fatalf("应回落 Degraded 兜底，得到 %v", msgs[0])
	}
}

// TestEnsureSystemFirstDeveloperInPlace 首条 developer：就地改 role，内容逐字保留
// （developer 同样被上游拒，报的是另一句 unapproved channel）。
func TestEnsureSystemFirstDeveloperInPlace(t *testing.T) {
	in, _ := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "developer", "content": "规则"},
			map[string]any{"role": "user", "content": "x"},
		},
	})
	msgs := decodeMsgs(t, EnsureSystemFirst(in, "IGNORED"))
	if len(msgs) != 2 {
		t.Fatalf("首条 developer 不应新增消息，得到 %d 条", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatal("developer 应就地归一为 system")
	}
	if msgs[0].(map[string]any)["content"] != "规则" {
		t.Fatal("内容必须逐字保留")
	}
}

// TestEnsureSystemFirstNoop 首条已 system / 无 messages / 坏 JSON → 原样返回。
func TestEnsureSystemFirstNoop(t *testing.T) {
	sys, _ := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "system", "content": "s"}},
	})
	if got := EnsureSystemFirst(sys, "x"); string(got) != string(sys) {
		t.Fatal("首条已 system 时应零改动")
	}

	empty, _ := json.Marshal(map[string]any{"messages": []any{}})
	if got := EnsureSystemFirst(empty, "x"); string(got) != string(empty) {
		t.Fatal("空 messages 不应凭空造对话")
	}

	bad := []byte("{oops")
	if got := EnsureSystemFirst(bad, "x"); string(got) != string(bad) {
		t.Fatal("坏 JSON 应原样返回")
	}
}
