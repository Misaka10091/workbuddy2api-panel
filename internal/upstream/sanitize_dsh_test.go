// sanitize_dsh_test.go DSH @ 路径说明段改写（11-128 抢救，上游 db31fc2 + 4f79fc8）。
//
// 背景：DSH 0.3.x system prompt 的 @ 路径段是**唯一不含品牌词**的 DSH 指纹，身份类规则
// 的预检根本不会命中，原文原样出站 → 400 11-128 Illegal API invocation from an
// unapproved channel。上游按「连续原文」匹配（只发首句 200、前两句连排 400），所以必须
// 整段一起同义改写。这里钉住七件事：两种措辞、跨行、嵌段保留前后文、幂等、段外 @ 不动、
// 全链路（prepareBody）、以及「不分角色」的全局层属性。
package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// 两种线上措辞的夹具（上游 sanitize_test.go 原文，逐字复用）。
//
// 命名按**结尾措辞**而不是按新旧：上游那两个常量名（dshPathsNow / dshPathsOld）与
// research §0 结论 2 / §1.2 的版本对应关系恰好相反，按名字推断版本会出错。实际版本
// 关系：`...path containing spaces.` 是 0.3.x 当前措辞，`...that contains spaces.` 更早。
const (
	// dshPathsCurrent 0.3.x 当前措辞：`absolute paths identify` +
	// `@"..." quotes a path containing spaces.`
	dshPathsCurrent = "Tokens prefixed with @ are paths the user explicitly referenced. " +
		"Relative paths resolve from the workspace root; absolute paths identify " +
		"files or directories on the host. A trailing slash marks a directory: " +
		"list it when its contents matter. Anything else is a file: use the read " +
		"tool when its contents are needed, and do not claim to have inspected it " +
		"before reading. @\"...\" quotes a path containing spaces."
	// dshPathsOlder 更早措辞：`absolute paths point to` +
	// `The @"..." form only quotes a path that contains spaces.`
	dshPathsOlder = "Tokens prefixed with @ are paths the user explicitly referenced. " +
		"Relative paths resolve from the workspace root; absolute paths point to " +
		"files or directories on the host. A trailing slash marks a directory, so " +
		"list it when its contents matter. Anything else is a file: open it with " +
		"the read tool when its contents are needed, and never claim to have " +
		"inspected it before reading. The @\"...\" form only quotes a path that " +
		"contains spaces."
)

// assertDshPathsRewritten 断言整段已被改写成 project root 版同义措辞。
// 三组断言缺一不可：原文特征消失（连续匹配失配）、改写措辞到位、project root 口径。
func assertDshPathsRewritten(t *testing.T, in, out string) {
	t.Helper()
	if strings.Contains(out, "Tokens prefixed with @") {
		t.Errorf("原文特征仍在（上游按连续原文拦）: %q", out)
	}
	if strings.Contains(out, "Relative paths resolve from the workspace root") {
		t.Errorf("旧措辞仍在: %q", out)
	}
	if !strings.Contains(out, "Paths beginning with @ refer to files the user explicitly mentioned.") {
		t.Errorf("改写串缺失: %q", out)
	}
	// project root 而非 workspace root：hub #677/#678 与 workbuddy-manager#312 的
	// 两说冲突下 project root 是安全交集，不许回退。
	if !strings.Contains(out, "resolved against the project root") {
		t.Errorf("应使用 project root 口径: %q", out)
	}
	if strings.Contains(out, "workspace root") {
		t.Errorf("不应回退成 workspace root: %q", out)
	}
}

// TestDshPathsCurrentWordingRewritten 0.3.x 当前措辞（结尾 `path containing spaces.`）
// 整段命中即改写。守护：11-128 现场 A 组（原段整段在 system → 400）必须被消除。
func TestDshPathsCurrentWordingRewritten(t *testing.T) {
	assertDshPathsRewritten(t, dshPathsCurrent, sanitizeText(dshPathsCurrent))
}

// TestDshPathsOlderWordingRewritten 更早的措辞（结尾 `path that contains spaces.`）也要
// 覆盖：正则尾部 `path (?:containing|that contains) spaces\.` 的双分支各吃一种。
func TestDshPathsOlderWordingRewritten(t *testing.T) {
	assertDshPathsRewritten(t, dshPathsOlder, sanitizeText(dshPathsOlder))
}

// TestDshPathsMultilineRewritten 段内本来就有换行：`(?s)` + 非贪婪 `.*?` 必须跨行吃满
// 整段，否则只改掉首句、前两句连排照样 400。
func TestDshPathsMultilineRewritten(t *testing.T) {
	in := strings.ReplaceAll(dshPathsCurrent, ". ", ".\n")
	if !strings.Contains(in, "\n") {
		t.Fatal("夹具应含换行")
	}
	assertDshPathsRewritten(t, in, sanitizeText(in))
}

// TestDshPathsEmbeddedKeepsSurroundings 嵌在长文本中间时只改这一整段，
// 前后文（含其它句子）必须一字不动。
func TestDshPathsEmbeddedKeepsSurroundings(t *testing.T) {
	const pre = "You are a coding assistant. 以下是路径约定说明。\n"
	const post = "\nRead the file before editing. Thanks."
	out := sanitizeText(pre + dshPathsCurrent + post)
	if !strings.HasPrefix(out, "You are a coding assistant.") {
		t.Errorf("前文被改动: %q", out)
	}
	if !strings.HasSuffix(out, "Read the file before editing. Thanks.") {
		t.Errorf("后文被改动: %q", out)
	}
	assertDshPathsRewritten(t, dshPathsCurrent, out)
}

// TestDshPathsIdempotent 幂等：改写串本身不再含预检字面量，重复净化结果不变
// （否则每次重试都重写一遍，diff 会漂且 TrimSpace 反复裁空白）。
func TestDshPathsIdempotent(t *testing.T) {
	once := sanitizeText(dshPathsCurrent)
	twice := sanitizeText(once)
	if once != twice {
		t.Errorf("不幂等:\n once=%q\ntwice=%q", once, twice)
	}
}

// TestDshPathsUnrelatedAtTextUntouched 段外 @ 零误伤：邮箱 / npm 包名 / Windows 路径
// 都不含「整句锚点」，预检不中 → 连正则都不跑，原样返回。
func TestDshPathsUnrelatedAtTextUntouched(t *testing.T) {
	cases := []string{
		"Mail me at someone@example.com and open C:\\cache\\@tmp.",
		"npm i @deepseek-ai/dsh-llm",
		// 关键时刻的守卫：光有「Relative paths resolve from the workspace root」不算指纹，
		// 必须整段齐（同时钉住「只发首句 200 / 前两句连排 400」的实测结论）。
		"Relative paths resolve from the workspace root, but this is not the paragraph.",
	}
	for _, in := range cases {
		if out := sanitizeText(in); out != in {
			t.Errorf("段外 @ 文本被误改: %q -> %q", in, out)
		}
	}
}

// TestDshPathsRewrittenRegardlessOfRole 该规则属**全局改写层**（不在 `if brand` 内）：
// 上游按连续原文匹配、不分角色，user 消息里的原文同样必 400，故 scope=identity
// （brandGlobal=false）时 user 消息也要被改写。
func TestDshPathsRewrittenRegardlessOfRole(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "SYS"},
		map[string]any{"role": "user", "content": dshPathsCurrent},
	}
	if !sanitizeMessages(msgs, false) {
		t.Fatal("user 消息里的 @ 段应被改写（全局层不受 brand 开关约束）")
	}
	got, _ := msgs[1].(map[string]any)["content"].(string)
	assertDshPathsRewritten(t, dshPathsCurrent, got)
}

// TestPrepareBodyRewritesDshPaths 全链路：出站 body（prepareBody 管线）里不得残留
// 「Tokens prefixed with @」这段连续原文。
func TestPrepareBodyRewritesDshPaths(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model":    "glm-5.2",
		"messages": []any{map[string]any{"role": "system", "content": dshPathsCurrent}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(PrepareBodyOpt(body, true))
	if strings.Contains(out, "Tokens prefixed with @") {
		t.Errorf("出站 body 仍含 DSH @ 段原文: %s", out)
	}
	if !strings.Contains(out, "project root") {
		t.Errorf("出站 body 缺少改写串: %s", out)
	}
}
