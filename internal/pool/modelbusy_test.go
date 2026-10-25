package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestCooldownModelBusyShortWindow 14003「模型繁忙」的停车窗口：
// 10s 起、同一窗口内重复命中不延长、不碰账号级 until。
func TestCooldownModelBusyShortWindow(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})

	const model = "glm-5.3"
	before := time.Now()
	p.CooldownModelBusy("u1", model, "14003 model busy")

	p.mu.Lock()
	e := p.byUID["u1"]
	mc, ok := e.modelCooldowns[model]
	accountUntil := e.until
	p.mu.Unlock()

	if !ok {
		t.Fatal("CooldownModelBusy 应写入 modelCooldowns[model]")
	}
	if got := mc.Until.Sub(before); got < 9*time.Second || got > 12*time.Second {
		t.Fatalf("首档应为 10s 量级，得到 %v", got)
	}
	if !accountUntil.IsZero() {
		t.Errorf("模型繁忙不得写账号级 until，得到 %v", accountUntil)
	}
	if e.healthyForModel(time.Now(), model) {
		t.Error("冷却窗口内该模型应被避开")
	}
	if !e.healthyForModel(time.Now(), "other-model") {
		t.Error("同号换模型应立即可用，不得被模型繁忙连带")
	}

	// 同一窗口内重复命中：不得把 10s 堆成更长。
	first := mc.Until
	p.CooldownModelBusy("u1", model, "14003 model busy")
	p.mu.Lock()
	mc2 := e.modelCooldowns[model]
	p.mu.Unlock()
	if !mc2.Until.Equal(first) {
		t.Fatalf("同一窗口内重复命中不得延长冷却：%v -> %v", first, mc2.Until)
	}
}

// TestCooldownModelBusyDoublesAndCaps 反复命中按 2 倍升级，封顶 1 分钟。
func TestCooldownModelBusyDoublesAndCaps(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	const model = "glm-5.3"

	// 每次命中前把已存窗口清空，模拟「窗口过期后再次命中」的阶梯升级。
	var last time.Duration
	for i := 0; i < 8; i++ {
		p.mu.Lock()
		if e := p.byUID["u1"]; e != nil {
			delete(e.modelCooldowns, model)
		}
		p.mu.Unlock()

		before := time.Now()
		p.CooldownModelBusy("u1", model, "14003 model busy")
		p.mu.Lock()
		mc := p.byUID["u1"].modelCooldowns[model]
		p.mu.Unlock()

		d := mc.Until.Sub(before)
		if d < last {
			t.Fatalf("第 %d 档不应变短：%v < %v", i+1, d, last)
		}
		last = d
	}
	if last > modelBusyMax+time.Second {
		t.Fatalf("封顶应为 %v，得到 %v", modelBusyMax, last)
	}
}

// TestBlockModelClearModelForbidden 11158 与 11102 一样是确定性负缓存，
// 权限恢复后成功请求必须能清掉。
func TestBlockModelClearModelForbidden(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	const model = "gpt-5.6-sol"

	p.mu.Lock()
	e := p.byUID["u1"]
	e.modelCooldowns = map[string]modelCooldown{
		model: {Until: time.Now().Add(time.Hour), Reason: "11158 model not allowed for account type"},
	}
	p.mu.Unlock()

	p.BlockModelClear("u1", model)

	p.mu.Lock()
	_, exists := p.byUID["u1"].modelCooldowns[model]
	p.mu.Unlock()
	if exists {
		t.Fatal("11158 负缓存应被 BlockModelClear 清除")
	}
}
