// cn_billing_base_test.go cn.billing_base 回填键（2026-10-28 新增）。
//
// 背景：billing 域默认从 www.codebuddy.cn 切到 www.workbuddy.cn（5.9.3 真机 billing 族
// host 全是它），cn.billing_base 是退回旧域的逃生门。它与 cn.chat_base 同规格——装配期
// 写进 upstream.Client 字段、请求路径并发只读，所以必须**无条件**列入重启项。
//
// 三类断言与 cn_chat_base_test.go 同形：显式配置生效、缺省回落内置默认、老配置
// （没有该键）行为不变。
package main

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestApplyCNBillingBase 空 = 保持内置默认（workbuddy.cn）；非空 = 覆盖（旧域回填）。
func TestApplyCNBillingBase(t *testing.T) {
	up := &upstream.Client{BillingBaseCN: "https://www.workbuddy.cn"}
	applyCNBillingBase(up, &Config{})
	if up.BillingBaseCN != "https://www.workbuddy.cn" {
		t.Errorf("空 cn.billing_base 不应覆盖默认: %q", up.BillingBaseCN)
	}

	cfg := &Config{}
	cfg.CN.BillingBase = "https://www.codebuddy.cn"
	applyCNBillingBase(up, cfg)
	if up.BillingBaseCN != "https://www.codebuddy.cn" {
		t.Errorf("cn.billing_base 未生效: %q", up.BillingBaseCN)
	}

	// nil 安全：装配期防御，不 panic。
	applyCNBillingBase(nil, cfg)
	applyCNBillingBase(up, nil)
}

// TestCNBillingBaseRestartRequired cn.billing_base 是装配期字段，必须**恒定**列入清单。
//
// 只有新值非空才列入时，「覆盖值 → 清空」不会提示重启——文件保存成功、面板显示已生效，
// 运行中的 BillingBaseCN 却仍是旧覆盖值（与 cn.chat_base、upstream.user_agent 同一教训）。
func TestCNBillingBaseRestartRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
	}{
		{"未配置（空）", ""},
		{"覆盖值被清空（新配置为空）", ""},
		{"覆盖为旧域", "https://www.codebuddy.cn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.CN.BillingBase = tc.base
			found := false
			for _, f := range restartRequiredFields(cfg) {
				if f == "cn.billing_base" {
					found = true
				}
			}
			if !found {
				t.Errorf("cn.billing_base 必须始终列入重启项（当前值 %q）", tc.base)
			}
		})
	}
}

// TestParseCNBillingBase 配置解析：cn 节可读、缺省为空；只有 cn.chat_base 的老配置
// （升级前的 config.json）解析后 billing_base 仍为空 → 回落新默认，行为不变。
func TestParseCNBillingBase(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"cn":{"billing_base":"https://www.codebuddy.cn"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CN.BillingBase != "https://www.codebuddy.cn" {
		t.Errorf("cn.billing_base=%q", cfg.CN.BillingBase)
	}

	cfg, err = ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CN.BillingBase != "" {
		t.Errorf("缺省 cn.billing_base 应为空（回落到内置默认域），得到 %q", cfg.CN.BillingBase)
	}
	// Default() 也必须留空：空是「用内置默认」的唯一编码，写死旧域会让回填键失去意义。
	if d := Default(); d.CN.BillingBase != "" {
		t.Errorf("Default().CN.BillingBase 应为空，得到 %q", d.CN.BillingBase)
	}

	// 老配置（升级前只可能有 cn.chat_base）行为不变。
	cfg, err = ParseConfig([]byte(`{"cn":{"chat_base":"https://copilot.tencent.com"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CN.BillingBase != "" {
		t.Errorf("老配置不应产生 billing_base，得到 %q", cfg.CN.BillingBase)
	}
	if cfg.CN.ChatBase != "https://copilot.tencent.com" {
		t.Errorf("老配置的 cn.chat_base 被影响: %q", cfg.CN.ChatBase)
	}
}
