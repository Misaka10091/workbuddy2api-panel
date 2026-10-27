// cn_chat_base_test.go CN 主域缺省与 cn.chat_base 回填键（上游 741cf59 同口径）。
package main

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestApplyCNChatBase 空 = 保持内置默认；非空 = 覆盖。
func TestApplyCNChatBase(t *testing.T) {
	up := &upstream.Client{ChatBaseCN: "https://www.workbuddy.cn"}
	applyCNChatBase(up, &Config{})
	if up.ChatBaseCN != "https://www.workbuddy.cn" {
		t.Errorf("空 cn.chat_base 不应覆盖默认: %q", up.ChatBaseCN)
	}

	cfg := &Config{}
	cfg.CN.ChatBase = "https://copilot.tencent.com"
	applyCNChatBase(up, cfg)
	if up.ChatBaseCN != "https://copilot.tencent.com" {
		t.Errorf("cn.chat_base 未生效: %q", up.ChatBaseCN)
	}

	// nil 安全：装配期防御，不 panic。
	applyCNChatBase(nil, cfg)
	applyCNChatBase(up, nil)
}

// TestCNChatBaseRestartRequired cn.chat_base 是装配期字段，必须**恒定**列入清单。
//
// 为什么空值也要提示：restartRequiredFields 返回的是「整类清单」而非本次 diff，
// 只有新值非空才列入时，「覆盖值 → 清空」不会提示——文件保存成功、面板显示已生效，
// 运行中的 ChatBaseCN 却仍是旧覆盖值。这个非对称场景与 upstream.user_agent 同源。
func TestCNChatBaseRestartRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
	}{
		{"未配置（空）", ""},
		{"覆盖值被清空（新配置为空）", ""},
		{"覆盖为备选入口", "https://copilot.tencent.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.CN.ChatBase = tc.base
			found := false
			for _, f := range restartRequiredFields(cfg) {
				if f == "cn.chat_base" {
					found = true
				}
			}
			if !found {
				t.Errorf("cn.chat_base 必须始终列入重启项（当前值 %q）", tc.base)
			}
		})
	}
}

// TestParseCNChatBase 配置解析：cn 节可读、缺省为空。
func TestParseCNChatBase(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"cn":{"chat_base":"https://copilot.tencent.com"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CN.ChatBase != "https://copilot.tencent.com" {
		t.Errorf("cn.chat_base=%q", cfg.CN.ChatBase)
	}

	cfg, err = ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CN.ChatBase != "" {
		t.Errorf("缺省 cn.chat_base 应为空（回落到内置主域），得到 %q", cfg.CN.ChatBase)
	}
}
