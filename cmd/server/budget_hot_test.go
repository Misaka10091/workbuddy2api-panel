package main

import (
	"strings"
	"testing"
)

// TestRestartRequiredFieldsExcludesBudget 预算闸必须热生效：保存配置后直接改运行中的
// dailyBudget（见 saveConfig 的 budgetHook），不再要求重启。
//
// 守护语义：异常耗分的特点是「几小时内烧穿当日上限」，而"保存 → 提示需要重启 →
// 人工挑时间重启"的窗口里账还在往外流。旧实现把三项列为装配期字段，是这块唯一的
// 止损手段，等于没有止损。
func TestRestartRequiredFieldsExcludesBudget(t *testing.T) {
	cfg := &Config{}
	cfg.Budget.DailyLimit = 1000
	cfg.Budget.BurstLimit = 120
	cfg.Budget.CacheLowRate = 30

	for _, f := range restartRequiredFields(cfg) {
		if strings.HasPrefix(f, "budget.") {
			t.Errorf("预算字段 %q 不应再要求重启（已接线 SetBudget*，保存即生效）", f)
		}
	}
}
