// move_test.go MoveSystemToUser（11-128 第二层抢救）。
//
// 背景：拆句（SplitSystemMessages）留在 system 最好；拆不动（单句 system）才把原文搬进
// 首条 user、system 换成中性提示词（上游 49f9db4 实现、e4dd458 降为第二层）。
//
// 契约：拼接形态固定 sysText + "\n\n" + txt（原文一个字不改）；首条 role 强制写 system
// （developer 也写 system——上游 role 白名单不含 developer，而 messages[0] 必须是 system
// 是渠道校验的硬要求）；结构异常一律**原样返回 body**，绝不硬改。
package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

const moveNeutral = "You are a helpful assistant."

// TestMoveSystemToUserKeepsText 成功路径：system 原文搬到首条 user 前部、system 换成
// 中性提示词、其余消息逐字不动。
func TestMoveSystemToUserKeepsText(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"system","content":"SYS TEXT"},` +
		`{"role":"assistant","content":"a"},{"role":"user","content":"hi"}]}`)
	out, ok := MoveSystemToUser(in, moveNeutral)
	if !ok {
		t.Fatal("应搬得动")
	}
	msgs := decodeMsgList(t, out)
	if len(msgs) != 3 {
		t.Fatalf("messages=%d want 3（不增删消息）: %s", len(msgs), out)
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != moveNeutral {
		t.Errorf("首条应为中性 system: %#v", msgs[0])
	}
	if got := msgs[2]["content"]; got != "SYS TEXT\n\nhi" {
		t.Errorf("首条 user 应为 sysText+\\n\\n+txt，得到 %#v", got)
	}
	if msgs[1]["content"] != "a" {
		t.Errorf("中间消息不应改动: %#v", msgs[1])
	}
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if obj["model"] != "m" {
		t.Errorf("其它字段应保留: %#v", obj["model"])
	}
}

// TestMoveSystemToUserDeveloper developer 首条同样处理，且**写成 system**
// （上游不认 developer，EnsureSystemFirst 口径一致）。
func TestMoveSystemToUserDeveloper(t *testing.T) {
	in := []byte(`{"messages":[{"role":"developer","content":"DEV"},{"role":"user","content":"hi"}]}`)
	out, ok := MoveSystemToUser(in, moveNeutral)
	if !ok {
		t.Fatal("developer 首条应搬得动")
	}
	msgs := decodeMsgList(t, out)
	if msgs[0]["role"] != "system" {
		t.Errorf("首条 role 必须写成 system，得到 %v", msgs[0]["role"])
	}
	if got := msgs[1]["content"]; got != "DEV\n\nhi" {
		t.Errorf("首条 user 应携带原文，得到 %#v", got)
	}
}

// TestMoveSystemToUserBails 全部失败路径都原样返回 body（绝不硬改结构异常的请求）。
//
// 10 例 = prompt.go 注释里归并的 9 条语义路径 + 「空 messages」单列（它与「无 messages」
// 走同一条 return，但输入形态不同，分开钉）。逐条：空 body / neutral 空 / 坏 JSON /
// 无 messages / 空 messages / 首条不是 system|developer / 首条 content 不是 string /
// 首条 content 全空白 / 找不到 user / 首条 user 是多模态 parts（往 parts 里插文本会打乱
// part 顺序）。
func TestMoveSystemToUserBails(t *testing.T) {
	cases := []struct {
		name    string
		in      []byte
		neutral string
	}{
		{"空 body", nil, moveNeutral},
		{"neutral 空", []byte(`{"messages":[{"role":"system","content":"S"}]}`), ""},
		{"坏 JSON", []byte(`{not json`), moveNeutral},
		{"无 messages", []byte(`{"model":"m"}`), moveNeutral},
		{"空 messages", []byte(`{"messages":[]}`), moveNeutral},
		{"首条不是 system", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), moveNeutral},
		{"首条 content 非 string", []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"S"}]},{"role":"user","content":"hi"}]}`), moveNeutral},
		{"首条 content 全空白", []byte(`{"messages":[{"role":"system","content":"   "},{"role":"user","content":"hi"}]}`), moveNeutral},
		{"找不到 user", []byte(`{"messages":[{"role":"system","content":"S"},{"role":"assistant","content":"a"}]}`), moveNeutral},
		{"首条 user 是多模态", []byte(`{"messages":[{"role":"system","content":"S"},{"role":"assistant","content":"a"},{"role":"user","content":[{"type":"text","text":"hi"}]}]}`), moveNeutral},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, ok := MoveSystemToUser(c.in, c.neutral)
			if ok {
				t.Errorf("不应搬动: %s", out)
			}
			if string(out) != string(c.in) {
				t.Errorf("失败时必须原样返回:\n got=%s\nwant=%s", out, c.in)
			}
		})
	}
}

// TestMoveSystemToUserChainWithSplit 三层衔接：多句 system 先进拆句层（搬家层不该被
// 触发）；拆句后的首条 system 若仍是单句，搬家层才接手。用公开函数表达调用方的阶梯。
func TestMoveSystemToUserChainWithSplit(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"First one. Second one."},{"role":"user","content":"hi"}]}`)
	split, ok := SplitSystemMessages(in)
	if !ok {
		t.Fatal("多句 system 应先拆得动")
	}
	// 拆句后首条仍含句号但只有一句 → 搬家层可继续处理（第二层兜底仍然可用）。
	moved, ok := MoveSystemToUser(split, moveNeutral)
	if !ok {
		t.Fatal("拆句后的单句 system 应能被搬家层接手")
	}
	msgs := decodeMsgList(t, moved)
	if msgs[0]["content"] != moveNeutral {
		t.Errorf("首条应为中性 system: %#v", msgs[0])
	}
	if !strings.HasPrefix(msgs[2]["content"].(string), "First one.\n\n") {
		t.Errorf("搬运应带上系统原文: %#v", msgs[2]["content"])
	}
}
