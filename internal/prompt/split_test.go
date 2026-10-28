// split_test.go SplitSystemMessages（11-128 首选抢救层）。
//
// 背景：上游按「连续原文」匹配指纹，句界一断就失配，而拆句**文本一字不改、承载通道仍是
// system**（模型指令优先级不变）——所以它是三层抢救里改动最小的一层（来源：上游 49f9db4
// + e4dd458 重排；真机 A–G 对照见 research §4.4：原段 400 / 拆开 200）。
//
// 关键约束：**只拆最长的那条** system/developer。人格句通常短，拆开会废掉指令
// （G 组 ARRR 0/3 是硬证据）；真正撞墙的长段落正好是最长那条。
package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeMsgList 解出 messages 列表（测试辅助）。
func decodeMsgList(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("body not json: %v (%s)", err, body)
	}
	raw, ok := obj["messages"].([]any)
	if !ok {
		t.Fatalf("messages not []any: %#v", obj["messages"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		mm, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("msg not map: %#v", m)
		}
		out = append(out, mm)
	}
	return out
}

const (
	splitLongSys = "First instruction sentence here. Second instruction sentence here. Third instruction sentence here."
	// splitPersona 短但**多句**的人格句：正是 G 组（人格句拆成两条 system → ARRR 0/3）的
	// 现场形态。单句人格句硬造不出「拆所有」与「只拆最长」的差别，必须用多句短句。
	splitPersona = "You are a pirate. Answer briefly."
)

// TestSplitSystemMessagesKeepsTextInSystem 拆句只按句界切：文本一字不改（拼回去等于
// 原文，只有句界空白被吃）、每条承载仍是 system、其余消息位置不变。
func TestSplitSystemMessagesKeepsTextInSystem(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"system","content":"` + splitLongSys + `"},{"role":"user","content":"hi"}]}`)
	out, ok := SplitSystemMessages(in)
	if !ok {
		t.Fatal("多句 system 应拆得动")
	}
	msgs := decodeMsgList(t, out)
	if len(msgs) != 4 {
		t.Fatalf("messages=%d want 4 (3 system + 1 user): %s", len(msgs), out)
	}
	var joined []string
	for i := 0; i < 3; i++ {
		if msgs[i]["role"] != "system" {
			t.Fatalf("messages[%d].role=%v want system", i, msgs[i]["role"])
		}
		s, ok := msgs[i]["content"].(string)
		if !ok {
			t.Fatalf("messages[%d].content 不是 string: %#v", i, msgs[i]["content"])
		}
		joined = append(joined, s)
	}
	if got := strings.Join(joined, " "); got != splitLongSys {
		t.Errorf("文本被改动（只许按句界切）:\n got=%q\nwant=%q", got, splitLongSys)
	}
	if msgs[3]["role"] != "user" || msgs[3]["content"] != "hi" {
		t.Errorf("user 消息应原样保留: %#v", msgs[3])
	}
}

// TestSplitSystemMessagesBailsOnSingleSentence 单句 system 拆不动：返回 (body,false)
// 且字节完全一致，交给调用方走搬运/替换层。
func TestSplitSystemMessagesBailsOnSingleSentence(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"You are a pirate."},{"role":"user","content":"hi"}]}`)
	out, ok := SplitSystemMessages(in)
	if ok {
		t.Fatalf("单句 system 不应拆分: %s", out)
	}
	if string(out) != string(in) {
		t.Errorf("失败时必须原样返回: %s", out)
	}
}

// TestSplitSystemMessagesOnlyLongest 只拆最长那条：短的（人格句）原样留在原位**一条**，
// 长 3 句拆成 3 条 → 总消息数 1 短句 + 3 + 1 user = 5。
//
// 这条钉住 G 组结论：人格句一旦被拆就失效（ARRR 0/3）。人格句故意写成**短但两句**——
// 只有这样，「拆所有 system」的错误实现才会被这条断言抓住（单句人格句拆完仍是一条，
// 看不出差别）。
func TestSplitSystemMessagesOnlyLongest(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"` + splitPersona + `"},` +
		`{"role":"system","content":"` + splitLongSys + `"},{"role":"user","content":"hi"}]}`)
	out, ok := SplitSystemMessages(in)
	if !ok {
		t.Fatal("应拆得动（最长那条是多句）")
	}
	msgs := decodeMsgList(t, out)
	if len(msgs) != 5 {
		t.Fatalf("messages=%d want 5: %s", len(msgs), out)
	}
	if msgs[0]["content"] != splitPersona || msgs[0]["role"] != "system" {
		t.Errorf("短 system 不得被拆/被移动: %#v", msgs[0])
	}
	if msgs[4]["role"] != "user" || msgs[4]["content"] != "hi" {
		t.Errorf("user 消息应原样保留: %#v", msgs[4])
	}
}

// TestSplitSystemMessagesDeveloperBecomesSystem developer 也能被拆，拆出的每条固定
// 写 role=system（上游 role 白名单不含 developer；归一在 payload.normalizeRoles 里也做）。
func TestSplitSystemMessagesDeveloperBecomesSystem(t *testing.T) {
	in := []byte(`{"messages":[{"role":"developer","content":"` + splitLongSys + `"},{"role":"user","content":"hi"}]}`)
	out, ok := SplitSystemMessages(in)
	if !ok {
		t.Fatal("developer 多句应拆得动")
	}
	msgs := decodeMsgList(t, out)
	if len(msgs) != 4 {
		t.Fatalf("messages=%d want 4: %s", len(msgs), out)
	}
	for i := 0; i < 3; i++ {
		if msgs[i]["role"] != "system" {
			t.Errorf("messages[%d].role=%v want system", i, msgs[i]["role"])
		}
	}
}

// TestSplitSystemMessagesNoSystemBails 没有 system/developer、坏 JSON、空 body、
// 空 messages 一律原样返回 false（绝不在结构异常的请求上硬改）。
func TestSplitSystemMessagesNoSystemBails(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"空 body", nil},
		{"坏 JSON", []byte(`{not json`)},
		{"无 messages", []byte(`{"model":"m"}`)},
		{"空 messages", []byte(`{"messages":[]}`)},
		{"只有 user", []byte(`{"messages":[{"role":"user","content":"a. b."}]}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, ok := SplitSystemMessages(c.in)
			if ok {
				t.Errorf("不应拆分: %s", out)
			}
			if string(out) != string(c.in) {
				t.Errorf("失败时必须原样返回: %s", out)
			}
		})
	}
}

// TestSplitSentencesIgnoresNonSentenceDots 句界只认 . ! ? 后的空白/换行：
// 版本号与域名里的点不切（"1.2.3" / "example.com"），否则会把一句指令切碎。
func TestSplitSentencesIgnoresNonSentenceDots(t *testing.T) {
	parts := splitSentences("Use version 1.2.3 from example.com then stop. Next one.")
	if len(parts) != 2 {
		t.Fatalf("parts=%d want 2: %#v", len(parts), parts)
	}
	if parts[0] != "Use version 1.2.3 from example.com then stop." {
		t.Errorf("parts[0]=%q", parts[0])
	}
	if parts[1] != "Next one." {
		t.Errorf("parts[1]=%q", parts[1])
	}
}

// TestSplitSentencesKeepsPunctuation 标点保留在段尾、段两端空白丢：
// 文本语义与可读性不变（上游按原文匹配，标点也是原文的一部分）。
func TestSplitSentencesKeepsPunctuation(t *testing.T) {
	parts := splitSentences("  First!  Second?  Third.  ")
	want := []string{"First!", "Second?", "Third."}
	if len(parts) != len(want) {
		t.Fatalf("parts=%#v want %#v", parts, want)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("parts[%d]=%q want %q", i, parts[i], want[i])
		}
	}
}
