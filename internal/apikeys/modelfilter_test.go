package apikeys

import (
	"strings"
	"testing"
)

// TestGlobMatch 通配符语义：* 任意长（含空）、? 单字符、无通配即精确、大小写不敏感。
//
// 实现是 path.Match 的封装（不是手写回溯器），所以这里同时钉住「标准库语义」：
// 若将来换实现，这些用例保证行为不变。
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		// 精确
		{"hy3", "hy3", true},
		{"hy3", "hy3x", false},
		{"hy3", "hy", false},
		// * 后缀
		{"glm-*", "glm-5.2", true},
		{"glm-*", "glm-5.3-flash", true},
		{"glm-*", "glm-", true}, // * 可匹配空
		{"glm-*", "hy3", false},
		// * 前缀
		{"*-sol", "gpt-6-sol", true},
		{"*-sol", "gpt-6", false},
		// * 中间
		{"gpt-*-sol", "gpt-6.1-sol", true},
		{"gpt-*-sol", "gpt-6-sol", true},
		{"gpt-*-sol", "gpt--sol", true},  // * 匹配空串（两侧 '-' 都在）
		{"gpt-*-sol", "gpt-sol", false},  // 少一个 '-'：pattern 需要两个
		{"gpt-*-sol", "gpt-6-luna", false},
		// 裸 * 匹配一切
		{"*", "", true},
		{"*", "anything", true},
		// ? 单字符
		{"gpt-?-sol", "gpt-6-sol", true},
		{"gpt-?-sol", "gpt-6.1-sol", false},
		{"hy?", "hy3", true},
		{"hy?", "hy33", false},
		// 组合
		{"glm-?.?-*", "glm-5.3-flash", true},
		{"glm-?.?-*", "glm-5-flash", false},
		// 大小写不敏感
		{"GLM-*", "glm-5.2", true},
		{"glm-*", "GLM-5.2", true},
		// 多个 *
		{"*-*", "a-b", true},
		{"**", "x", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pattern, c.s, got, c.want)
		}
	}
}

// TestGlobMatchNoReDoS 病态 pattern 不能让匹配退化成指数时间。
// path.Match 内部是逐段线性扫描（遇 '*' 时取到下一个字面量再定位），
// 这里用长输入钉住——若将来有人换回手写递归实现，这条会先挂住。
func TestGlobMatchNoReDoS(t *testing.T) {
	pattern := "a*a*a*a*a*a*a*a*a*a*b"
	s := strings.Repeat("a", 200)
	if globMatch(pattern, s) {
		t.Error("不该匹配（结尾要 b）")
	}
	// 同上但能匹配
	if !globMatch(pattern, s+"b") {
		t.Error("应匹配")
	}
}

// TestValidatePatternsRejectsBadSyntax 非法 pattern 必须在保存时被拒。
//
// 背景：我最初声称"glob 没有非法形态、任何字符都按字面量处理"——这是错的。
// '[' 未闭合就是语法错误，而 path.Match 会返回 error。若不校验：
// 该条规则永远不匹配（filter 里 error 当不匹配处理），运维以为限制生效、
// 实际全放行 —— 典型的静默失效。
func TestValidatePatternsRejectsBadSyntax(t *testing.T) {
	cases := []struct {
		pats    []string
		wantErr bool
	}{
		{[]string{"glm-*", "hy3"}, false},
		{[]string{"gpt-?.?-*"}, false},
		{[]string{""}, false},        // 空串被跳过，不算错
		{[]string{"  "}, false},      // 纯空白同理
		{[]string{"cn:glm-*"}, false}, // 带域前缀合法
		{[]string{"glm-["}, true},     // 未闭合的字符类
		{[]string{"a[b"}, true},
		{[]string{"ok", "bad["}, true}, // 一条坏就整体报错
	}
	for _, c := range cases {
		errs := ValidatePatterns(c.pats)
		if (len(errs) > 0) != c.wantErr {
			t.Errorf("ValidatePatterns(%v)=%v wantErr=%v", c.pats, errs, c.wantErr)
		}
	}
}

// TestStoreRejectsInvalidPattern Add 时拒绝非法 pattern（不能存进去再静默失效）。
func TestStoreRejectsInvalidPattern(t *testing.T) {
	s, _ := New(nil)
	if _, err := s.Add(Key{
		Name: "坏规则", Key: "wb-badpattern00001", Realm: RealmCN,
		AllowModels: []string{"glm-["},
	}); err == nil {
		t.Error("Add 未拒绝非法 pattern")
	}
	if s.Len() != 0 {
		t.Errorf("非法 pattern 的密钥仍被写入: Len=%d", s.Len())
	}
}

// TestStoreUpdateRejectsInvalidPattern Update 时拒绝，且保持条目原样。
func TestStoreUpdateRejectsInvalidPattern(t *testing.T) {
	key := "wb-okpattern0000001"
	s, _ := New([]Key{{
		Name: "ok", Key: key, Realm: RealmCN, AllowModels: []string{"glm-*"},
	}})

	bad := []string{"glm-["}
	if _, err := s.Update(key, KeyUpdate{AllowModels: &bad}); err == nil {
		t.Error("Update 未拒绝非法 pattern")
	}
	// 原规则必须完好（不能改一半）
	k, _ := s.Lookup(key)
	if len(k.AllowModels) != 1 || k.AllowModels[0] != "glm-*" {
		t.Errorf("失败更新污染了原规则: %v", k.AllowModels)
	}
}

// TestFilterAllowsSemantics 白/黑名单组合语义。
func TestFilterAllowsSemantics(t *testing.T) {
	t.Run("空规则不限制", func(t *testing.T) {
		f := compileFilter(nil, nil)
		if f.Active() {
			t.Error("空规则不该 Active")
		}
		for _, m := range []string{"glm-5.2", "", "anything"} {
			if !f.Allows(m) {
				t.Errorf("空规则应允许 %q", m)
			}
		}
	})

	t.Run("只有白名单", func(t *testing.T) {
		f := compileFilter([]string{"glm-*"}, nil)
		if !f.Allows("glm-5.2") || !f.Allows("glm-5.3-flash") {
			t.Error("白名单应允许 glm 系列")
		}
		if f.Allows("hy3") {
			t.Error("白名单应拒绝非 glm 模型")
		}
	})

	t.Run("只有黑名单", func(t *testing.T) {
		f := compileFilter(nil, []string{"gpt-*"})
		if f.Allows("gpt-6-sol") {
			t.Error("黑名单应拒绝 gpt 系列")
		}
		if !f.Allows("glm-5.2") {
			t.Error("黑名单不该影响未命中模型")
		}
	})

	t.Run("黑名单优先于白名单", func(t *testing.T) {
		// 「白名单 glm-* + 黑名单 glm-5.1」= 除 5.1 之外的 glm 系列。
		// 这是运维最常用的组合，顺序错了就表达不出来。
		f := compileFilter([]string{"glm-*"}, []string{"glm-5.1"})
		if !f.Allows("glm-5.2") {
			t.Error("glm-5.2 应允许")
		}
		if f.Allows("glm-5.1") {
			t.Error("glm-5.1 命中黑名单应拒绝（黑名单优先）")
		}
		if f.Allows("hy3") {
			t.Error("hy3 不在白名单应拒绝")
		}
	})

	t.Run("剥域前缀", func(t *testing.T) {
		// 用户照抄 /v1/models 的 "cn:glm-5.2" 也能匹配到裸名。
		f := compileFilter([]string{"cn:glm-*"}, nil)
		if !f.Allows("glm-5.2") {
			t.Error("pattern 带域前缀时应剥掉再匹配")
		}
		// 反方向：判定时传入带前缀的模型名也应命中。
		f2 := compileFilter([]string{"glm-*"}, nil)
		if !f2.Allows("global:glm-5.3") {
			t.Error("判定值带域前缀时应剥掉再匹配")
		}
	})

	t.Run("空串与空白被剔除", func(t *testing.T) {
		f := compileFilter([]string{"", "  ", "glm-*"}, []string{"\t"})
		if !f.Allows("glm-5.2") {
			t.Error("有效 pattern 应保留")
		}
		if f.Allows("hy3") {
			t.Error("空白 pattern 不该被当成 * 放行一切")
		}
	})
}

// TestNilFilterSafe 零值 filter 必须安全（主密钥路径传的就是 nil）。
func TestNilFilterSafe(t *testing.T) {
	var f *ModelFilter
	if f.Active() {
		t.Error("nil filter 不该 Active")
	}
	if !f.Allows("anything") {
		t.Error("nil filter 应允许一切")
	}
}

// TestKeyModelAllowed 密钥级判定入口（handler 与面板共用）。
func TestKeyModelAllowed(t *testing.T) {
	k := Key{AllowModels: []string{"glm-*"}}
	k.AllowModels = normalizePatterns(k.AllowModels)
	k.filter = compileFilter(k.AllowModels, nil)
	if !k.ModelAllowed("glm-5.2") {
		t.Error("应允许 glm-5.2")
	}
	if k.ModelAllowed("hy3") {
		t.Error("应拒绝 hy3")
	}
	// 无规则密钥恒允许
	plain := Key{}
	if !plain.ModelAllowed("whatever") {
		t.Error("无规则密钥应允许一切")
	}
}
