// sanitize_brand_test.go 身份类脱敏（品牌词）与作用域护栏。
package upstream

import (
	"strings"
	"testing"
)

func TestSanitizeBrandIdentityRewrite(t *testing.T) {
	in := "You are DeepSeek Harness, a terminal coding agent."
	// 全局规则不碰品牌词：品牌词规则只在应用身份类规则时生效。
	if got := sanitizeText(in); got != in {
		t.Errorf("sanitizeText 不应改写品牌词: %q", got)
	}
	got := sanitizeTextIdentity(in)
	if !strings.Contains(got, "DeepSeekRuntime") || strings.Contains(got, "DeepSeek Harness") {
		t.Errorf("身份类脱敏未改写品牌词: %q", got)
	}
}

func TestSanitizeBrandScopeIdentityOnly(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "You are DeepSeek Harness."},
		map[string]any{"role": "user", "content": "DeepSeek Harness 是什么"},
	}
	if !sanitizeMessages(msgs, false) {
		t.Fatal("system 消息应被改写")
	}
	sys, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "DeepSeekRuntime") {
		t.Errorf("system 未被改写: %q", sys)
	}
	if usr, _ := msgs[1].(map[string]any)["content"].(string); usr != "DeepSeek Harness 是什么" {
		t.Errorf("scope=identity 时 user 应原样透传: %q", usr)
	}
}

func TestSanitizeBrandScopeAll(t *testing.T) {
	msgs := []any{map[string]any{"role": "user", "content": "DeepSeek Harness"}}
	if !sanitizeMessages(msgs, true) {
		t.Fatal("scope=all 时 user 也应被改写")
	}
	if got, _ := msgs[0].(map[string]any)["content"].(string); got != "DeepSeekRuntime" {
		t.Errorf("scope=all 改写结果=%q", got)
	}
}

// TestSanitizeBrandDoesNotTrimPlainText 只含品牌词以外普通文本的消息不应被 TrimSpace
// （快速路径必须原样返回，用户输入的空白属于用户数据）。
func TestSanitizeBrandDoesNotTrimPlainText(t *testing.T) {
	in := "  hello world  "
	if got := sanitizeTextWith(in, false); got != in {
		t.Errorf("普通文本被改动: %q", got)
	}
}
