package server

import (
	"strings"
	"testing"
)

// TestRealmModelNamePrefix 域标识必须是**前缀**且用方括号短标签。
//
// 为什么钉前缀而不是后缀：客户端模型选择器普遍按固定宽度截断长名称（保留开头、
// 尾部省略），后缀式的 "GLM-5.3 (国际版)" 一被截就把域信息丢在最需要它的地方；
// 放开头则永远先看见域。这个测试就是防止有人「优化」回调成后缀。
func TestRealmModelNamePrefix(t *testing.T) {
	cases := []struct{ name, realm, want string }{
		{"Hy3", "cn", "[国内版] Hy3"},
		{"Hy3", "global", "[国际版] Hy3"},
		{"GLM-5.3", "cn", "[国内版] GLM-5.3"},
		{"Hy4 preview", "global", "[国际版] Hy4 preview"},
		// 非法/空域不编造标注
		{"Hy3", "", "Hy3"},
		{"Hy3", "jp", "Hy3"},
		{"", "cn", ""},
	}
	for _, c := range cases {
		if got := realmModelName(c.name, c.realm); got != c.want {
			t.Errorf("realmModelName(%q,%q)=%q want %q", c.name, c.realm, got, c.want)
		}
	}
}

// TestRealmModelNameIdempotent 重复调用不得累积标注。
//
// modelList 与 modelListForRealm 会各自过一遍 name 处理，若不做幂等就会产出
// "[国内版] [国内版] Hy3"。
func TestRealmModelNameIdempotent(t *testing.T) {
	once := realmModelName("Hy3", "cn")
	twice := realmModelName(once, "cn")
	if once != twice {
		t.Errorf("非幂等: 一次=%q 两次=%q", once, twice)
	}
}

// TestRealmModelNameStripsLegacySuffix 兼容旧的后缀形态（升级过渡期）。
// 旧版本输出 "Hy3 (国内版)"；新逻辑必须先剥旧后缀再加新前缀，
// 不能变成 "[国内版] Hy3 (国内版)"。
func TestRealmModelNameStripsLegacySuffix(t *testing.T) {
	cases := []struct{ name, realm, want string }{
		{"Hy3 (国内版)", "cn", "[国内版] Hy3"},
		{"GPT-6-Sol (国际版)", "global", "[国际版] GPT-6-Sol"},
		// 名称本身就带同形文字但不是域标注：只剥精确后缀，不误伤中间内容
		{"说明 (国内版) 补充", "cn", "[国内版] 说明 (国内版) 补充"},
	}
	for _, c := range cases {
		if got := realmModelName(c.name, c.realm); got != c.want {
			t.Errorf("realmModelName(%q,%q)=%q want %q", c.name, c.realm, got, c.want)
		}
	}
}

// TestRealmModelNamePrefixSurvivesTruncation 截断后仍能看到域（前缀的核心目的）。
// 用常见客户端列表宽度 12 个字符模拟：后缀方案会丢域信息，前缀方案保留。
func TestRealmModelNamePrefixSurvivesTruncation(t *testing.T) {
	prefixed := realmModelName("Deepseek-V4.1-Flash", "global")
	const width = 12
	if !strings.HasPrefix(prefixed, "[国际版]") {
		t.Fatalf("前缀不对: %q", prefixed)
	}
	cut := prefixed
	if len(cut) > width {
		cut = cut[:width] // 按字节截断（客户端常见的粗暴截断）
	}
	if !strings.Contains(cut, "国际") {
		t.Errorf("截断到 %d 字节后丢了域信息: %q", width, cut)
	}
	// 对照：旧的后缀方案同样截断必然丢域
	legacy := "Deepseek-V4.1-Flash (国际版)"
	if strings.Contains(legacy[:width], "国际") {
		t.Error("对照组不成立：后缀方案在此宽度下仍保留域信息，用例失去判别力")
	}
}

// TestFilterEntriesByRealm 绑域密钥的模型条目：只留本域，且 id 去域前缀、
// name 去域标注（该密钥只能看到这一个域，标注是噪音）。
func TestFilterEntriesByRealm(t *testing.T) {
	all := []map[string]any{
		{"id": "cn:glm-5.3", "name": "[国内版] GLM-5.3", "realm": "cn"},
		{"id": "global:glm-5.3", "name": "[国际版] GLM-5.3", "realm": "global"},
		{"id": "cn:hy3", "name": "[国内版] Hy3", "realm": "cn"},
		{"id": "global:gpt-6-sol", "name": "[国际版] GPT-6-Sol", "realm": "global"},
	}

	cn := filterEntriesByRealm(all, "cn")
	if len(cn) != 2 {
		t.Fatalf("cn 过滤=%d 条 want 2", len(cn))
	}
	for _, e := range cn {
		id := e["id"].(string)
		if strings.Contains(id, ":") {
			t.Errorf("id 未去前缀: %q", id)
		}
		if n := e["name"].(string); strings.Contains(n, "国内版") {
			t.Errorf("name 未去域标注: %q", n)
		}
	}

	gl := filterEntriesByRealm(all, "global")
	if len(gl) != 2 {
		t.Fatalf("global 过滤=%d 条 want 2", len(gl))
	}
	for _, e := range gl {
		if n := e["name"].(string); strings.Contains(n, "国际版") {
			t.Errorf("name 未去域标注: %q", n)
		}
	}

	// 未知域 → 空（不返回全量，避免越权看到别的域）
	if got := filterEntriesByRealm(all, "jp"); len(got) != 0 {
		t.Errorf("未知域过滤=%d 条 want 0", len(got))
	}
	// 空输入不 panic
	if got := filterEntriesByRealm(nil, "cn"); len(got) != 0 {
		t.Errorf("nil 输入=%d 条 want 0", len(got))
	}
}

// TestFilterEntriesByRealmDoesNotMutateInput 过滤必须返回副本：
// 就地改 all 会污染同一次请求里另一分支（主密钥视角的两域列表）。
func TestFilterEntriesByRealmDoesNotMutateInput(t *testing.T) {
	all := []map[string]any{
		{"id": "cn:glm-5.3", "name": "[国内版] GLM-5.3"},
	}
	_ = filterEntriesByRealm(all, "cn")
	if all[0]["id"] != "cn:glm-5.3" {
		t.Errorf("原条目 id 被就地改写: %q", all[0]["id"])
	}
	if all[0]["name"] != "[国内版] GLM-5.3" {
		t.Errorf("原条目 name 被就地改写: %q", all[0]["name"])
	}
}

// TestFilterEntriesByRealmStripsLegacySuffix 兼容升级过渡期的旧后缀 name。
func TestFilterEntriesByRealmStripsLegacySuffix(t *testing.T) {
	all := []map[string]any{
		{"id": "cn:hy3", "name": "Hy3 (国内版)"},
	}
	got := filterEntriesByRealm(all, "cn")
	if len(got) != 1 || got[0]["name"] != "Hy3" {
		t.Errorf("旧后缀未剥净: %v", got)
	}
}
