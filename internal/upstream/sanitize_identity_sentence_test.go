// sanitize_identity_sentence_test.go 身份句式整句中性化（11-128 抢救，上游 4f79fc8）。
//
// 背景：只把品牌词换掉、保留 "You are … powered by X." 的骨架仍被上游拦——11-128 认的
// 是句式不是品牌词。本文件钉住四件事：8 条变体命中、4 类误伤守卫（修饰词窗口 ≤3 的
// 上界作用）、`${1}` 句尾空白回填（RE2 无 (?=) 的绕法）、以及「只受 sanitize_brand_scope
// 约束」的作用域。
package upstream

import (
	"strings"
	"testing"
)

// TestIdentitySentenceVariants 八条变体都要命中并被换成中性句，且句后文本不丢。
//
// 覆盖维度：AI agent / coding agent（带点连字符模型名）/ AI assistant / 专名 + 逗号 +
// 动词 / 冠词 the + 专名 + 名词 + 逗号 / bot / tool / model —— 对应七种动词搭配的典型
// 线上形态。断言 `Next.` 仍在，顺带钉住 `${1}` 回填：否则 "assistant.Next." 会被粘住。
func TestIdentitySentenceVariants(t *testing.T) {
	cases := []string{
		"You are an AI agent powered by AcmeRuntime. Next.",
		"You are a coding agent powered by the deepseek-v4.1-flash model. Next.",
		"You are an AI assistant built on DeepSeek. Next.",
		"You are Acme Agent, built by Acme Corp. Next.",
		"You are the FooBot assistant, created by someone. Next.",
		"You are a bot developed by someone. Next.",
		"You are a tool made by someone. Next.",
		"You are a model trained by someone. Next.",
	}
	for _, in := range cases {
		if !hasIdentityFingerprint(in) {
			t.Errorf("身份句式预检未命中: %q", in)
		}
		out := sanitizeTextIdentity(in)
		if !strings.HasPrefix(out, "You are a helpful AI assistant. ") {
			t.Errorf("未整句中性化: %q -> %q", in, out)
		}
		if !strings.HasSuffix(out, "Next.") {
			t.Errorf("句后文本丢失: %q -> %q", in, out)
		}
		if strings.Contains(out, "assistant.Next") {
			t.Errorf("句尾空白未回填（${1} 失效）: %q", out)
		}
	}
}

// TestIdentitySentenceFalsePositives 四类误伤守卫：分词修饰宾语的普通句子必须原样。
//
// 机制：`You are` 后必须是「冠词 + ≤3 个修饰词 + 可选名词 + 动词搭配」，上界 3 就是
// 防线（"free to use tools built by" 等描述超过窗口）。断言两条：
// hasIdentityFingerprint 为 false（否则会白跑一趟 TrimSpace 裁掉用户空白）、
// sanitizeTextIdentity 不改写。
func TestIdentitySentenceFalsePositives(t *testing.T) {
	cases := []string{
		"You are free to use tools built by the vendor.",
		"You are a helpful assistant that uses services developed by others. Ok.",
		"You are welcome to deploy packages made by third parties.",
		"You are right, that module was built by the other team. Fine.",
	}
	for _, in := range cases {
		if hasIdentityFingerprint(in) {
			t.Errorf("误判为身份句式（会被 TrimSpace 裁空白）: %q", in)
		}
		if out := sanitizeTextIdentity(in); out != in {
			t.Errorf("普通讲解句被改写: %q -> %q", in, out)
		}
	}
}

// TestIdentitySentenceScope 作用域：身份句式只受 features.sanitize_brand_scope 控制。
// brand=false（全局层 / scope=identity 时的非身份消息）不动；brand=true 才动。
func TestIdentitySentenceScope(t *testing.T) {
	in := "You are an AI agent powered by AcmeRuntime. Go."
	if out := sanitizeText(in); out != in {
		t.Errorf("全局层不应改身份句式: %q -> %q", in, out)
	}
	if out := sanitizeTextIdentity(in); out == in {
		t.Errorf("身份路径应改身份句式: %q", out)
	}
}

// TestIdentitySentenceWithBrandNameFullyNeutralized 品牌全名与句式同时在场时，整句被
// 中性化、品牌名不再出现（两层叠加不留残渣）。
//
// 注（诚实澄清）：本断言对「先品牌词后句式」的执行顺序**不敏感**——正则用 `[^\n]*?`
// 吃掉动词之后的一切，不依赖品牌原文，所以两种顺序的可观测结果相同。代码里把句式放在
// `if brand` 块首行只为与上游保持一致（防御上游将来收紧匹配），不构成可观测契约。
func TestIdentitySentenceWithBrandNameFullyNeutralized(t *testing.T) {
	in := "You are an AI agent powered by DeepSeek Harness. Use the read tool."
	out := sanitizeTextIdentity(in)
	if !strings.HasPrefix(out, "You are a helpful AI assistant. ") {
		t.Errorf("整句未中性化: %q -> %q", in, out)
	}
	if !strings.Contains(out, "Use the read tool.") {
		t.Errorf("句后内容丢失: %q", out)
	}
	if strings.Contains(out, "DeepSeek Harness") {
		t.Errorf("品牌全名残留: %q", out)
	}
}

// TestPrepareBodyRewritesIdentitySentence 全链路：出站 body 里不得残留
// "... powered by <brand>." 的句式骨架。
func TestPrepareBodyRewritesIdentitySentence(t *testing.T) {
	in := "You are an AI agent powered by DeepSeek Harness. Be precise."
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + in + `"}]}`)
	out := string(PrepareBodyOpt(body, true))
	if strings.Contains(out, "powered by") {
		t.Errorf("出站 body 仍含身份句式骨架: %s", out)
	}
	if !strings.Contains(out, "You are a helpful AI assistant.") {
		t.Errorf("出站 body 缺少中性句: %s", out)
	}
}
