package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestModelBlockedForRealm 模型级阻塞判定必须与选号同口径（按 realm）。
//
// 场景：cn 域唯一的号对这个模型被锁，global 域还有健康号。
//   - 按域判定 → cn 应报「模型不可用」（客户端换模型才有用）；
//   - 全池判定 → 会被 global 的健康号掩盖成「不是模型阻塞」，于是报
//     no_healthy_account（"号全挂了"），把排查方向指错。
func TestModelBlockedForRealm(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "cn1"})                             // 域推断为 cn
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"}) // 域推断为 global
	p.BlockModelBackoff("cn1", "glm-5.3", "11102 model not found")

	if st := p.ModelBlockedForRealm("cn", "glm-5.3"); !st.Blocked {
		t.Errorf("cn 域仅有的号对该模型被锁 → 应判本域阻塞: %+v", st)
	}
	if st := p.ModelBlockedForRealm("global", "glm-5.3"); st.Blocked {
		t.Errorf("global 域号健康 → 不应判阻塞: %+v", st)
	}
	if st := p.ModelBlocked("glm-5.3"); st.Blocked {
		t.Errorf("全池口径：还有域能服务该模型 → 不应判阻塞: %+v", st)
	}
}

// TestModelBlockedForRealmEmptyDomainIsNotModelProblem 本域没有任何参与判定的号时，
// 返回"未阻塞"（真的没号），不得伪装成模型级阻塞。
func TestModelBlockedForRealmEmptyDomainIsNotModelProblem(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "cn1"})
	p.BlockModelBackoff("cn1", "glm-5.3", "11102 model not found")

	if st := p.ModelBlockedForRealm("global", "glm-5.3"); st.Blocked {
		t.Errorf("该域没有可判定账号时应返回未阻塞: %+v", st)
	}
}

// TestCooldownModelBusyWithWait 14003 带 Retry-After 时采信该时长，但仍钳到
// modelBusyMax（1m）：这个头也可能来自边缘/WAF，超长值会把间歇繁忙的模型停死。
func TestCooldownModelBusyWithWait(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})

	p.CooldownModelBusyWithWait("u1", "glm-5.3", "14003 model busy", 30*time.Second)
	p.mu.RLock()
	mc, ok := p.byUID["u1"].modelCooldowns["glm-5.3"]
	until := p.byUID["u1"].until
	p.mu.RUnlock()
	if !ok {
		t.Fatal("应写入模型级冷却条目")
	}
	if rem := time.Until(mc.Until); rem <= 25*time.Second || rem > 30*time.Second+time.Second {
		t.Errorf("应按 Retry-After=30s 停车，实际剩余 %v", rem)
	}
	if !until.IsZero() {
		t.Errorf("until=%v 应为零值（14003 只停模型）", until)
	}

	// 超长值钳到 1m。
	p2 := New("")
	p2.Add(&auth.Auth{UID: "u1"})
	p2.CooldownModelBusyWithWait("u1", "glm-5.3", "14003 model busy", 10*time.Minute)
	p2.mu.RLock()
	mc2 := p2.byUID["u1"].modelCooldowns["glm-5.3"]
	p2.mu.RUnlock()
	if rem := time.Until(mc2.Until); rem > modelBusyMax+time.Second {
		t.Errorf("Retry-After 超长应钳到 %v，实际剩余 %v", modelBusyMax, rem)
	}
}

// TestCooldownModelBusyWithWaitKeepsLaterDeadline 已有更晚截止（6004 墙钟 / 11102
// 负缓存）时不得被较短的 Retry-After 缩短：那是把模型提前放回限流窗口。
func TestCooldownModelBusyWithWaitKeepsLaterDeadline(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	later := time.Now().Add(30 * time.Minute)
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: later, Reason: "6004 model rate limit"},
	}
	p.mu.Unlock()

	p.CooldownModelBusyWithWait("u1", "glm-5.3", "14003 model busy", 10*time.Second)

	p.mu.RLock()
	mc := p.byUID["u1"].modelCooldowns["glm-5.3"]
	p.mu.RUnlock()
	if d := mc.Until.Sub(later); d < -time.Second || d > time.Second {
		t.Errorf("Until=%v want ~%v（不得被更短的 Retry-After 缩短）", mc.Until, later)
	}
}
