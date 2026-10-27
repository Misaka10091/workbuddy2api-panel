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

// TestCNChatBaseRestartRequired cn.chat_base 是装配期字段：配了才提示重启。
func TestCNChatBaseRestartRequired(t *testing.T) {
	empty := &Config{}
	for _, f := range restartRequiredFields(empty) {
		if f == "cn.chat_base" {
			t.Fatal("未配置 cn.chat_base 时不该提示重启")
		}
	}
	cfg := &Config{}
	cfg.CN.ChatBase = "https://copilot.tencent.com"
	found := false
	for _, f := range restartRequiredFields(cfg) {
		if f == "cn.chat_base" {
			found = true
		}
	}
	if !found {
		t.Error("配置 cn.chat_base 后应提示重启")
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
