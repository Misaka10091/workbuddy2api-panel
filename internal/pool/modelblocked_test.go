package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// ---------------------------------------------------------------------------
// ModelBlocked：区分「号都在但都对这个模型关闭」与「池子真没号」（issue #102a）
// ---------------------------------------------------------------------------

// TestModelBlockedAllAccountsCooled 全部账号都对该模型冷却 → Blocked，
// 且带出账号数、最早解封时间与上游原因。
func TestModelBlockedAllAccountsCooled(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	base := time.Now()
	// 两个号对同一模型都有冷却，解封时间不同 → 应取最早的。
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: base.Add(2 * time.Hour), Reason: "11102 model [glm-5.3] service info not found"},
	}
	p.byUID["u2"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: base.Add(1 * time.Hour), Reason: "11102 model [glm-5.3] service info not found"},
	}
	p.mu.Unlock()

	st := p.ModelBlocked("glm-5.3")
	if !st.Blocked {
		t.Fatalf("全部账号冷却时 Blocked 应为 true，得到 %+v", st)
	}
	if st.Count != 2 {
		t.Errorf("Count=%d want 2", st.Count)
	}
	if st.Reason == "" {
		t.Errorf("Reason 应带出上游原因，得到空")
	}
	// 最早解封应接近 1h 后（u2），不是 2h（u1）。
	if d := time.Until(st.Until); d > 70*time.Minute || d < 50*time.Minute {
		t.Errorf("Until=%v 应取最早解封（约 1h 后），得到 %v", st.Until, d)
	}
}

// TestModelBlockedOneAccountStillServes 只要还有账号能服务该模型，就不算模型级阻塞
// ——否则会把「在途占满/积分保底」导致的选号失败误报成模型问题。
func TestModelBlockedOneAccountStillServes(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: time.Now().Add(time.Hour), Reason: "11102 ..."},
	}
	p.mu.Unlock()

	if st := p.ModelBlocked("glm-5.3"); st.Blocked {
		t.Fatalf("u2 仍可服务该模型，不应判定为模型级阻塞：%+v", st)
	}
	// 但 u2 对该模型无冷却这一点必须是真的（防止测试自身构造错误）。
	p.mu.RLock()
	cooled := p.byUID["u2"].modelCooled(time.Now(), "glm-5.3")
	p.mu.RUnlock()
	if cooled {
		t.Fatalf("测试构造有误：u2 不该对该模型冷却")
	}
}

// TestModelBlockedDisabledDoesNotCount 禁用号不参与判定：它不可用与模型无关，
// 既不构成"还能服务"的证明，也不该被算进被挡账号数。
func TestModelBlockedDisabledDoesNotCount(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: time.Now().Add(time.Hour), Reason: "11102 ..."},
	}
	p.byUID["u2"].disabled = true // 唯一还能服务的号被禁用了
	p.mu.Unlock()

	st := p.ModelBlocked("glm-5.3")
	if !st.Blocked {
		t.Fatalf("除禁用号外全部冷却 → 应判定为模型级阻塞，得到 %+v", st)
	}
	if st.Count != 1 {
		t.Errorf("Count=%d want 1（禁用号不计入）", st.Count)
	}
}

// TestModelBlockedAuditOnlyNotBlocking AuditOnly 条目只审计不拦截，不能算阻塞
// ——口径必须与选号的 modelCooled 一致，否则会把"其实能调"的模型报成不可用。
func TestModelBlockedAuditOnlyNotBlocking(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: time.Now().Add(time.Hour), Reason: "audit", AuditOnly: true},
	}
	p.mu.Unlock()

	if st := p.ModelBlocked("glm-5.3"); st.Blocked {
		t.Fatalf("AuditOnly 条目不得判定为阻塞：%+v", st)
	}
}

// TestModelBlockedExpiredNotBlocking 已过期的冷却不算阻塞。
func TestModelBlockedExpiredNotBlocking(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: time.Now().Add(-time.Minute), Reason: "11102 ..."},
	}
	p.mu.Unlock()

	if st := p.ModelBlocked("glm-5.3"); st.Blocked {
		t.Fatalf("已过期冷却不得判定为阻塞：%+v", st)
	}
}

// TestModelBlockedEmptyInputs 空模型名与空池都返回"未阻塞"：前者无从判断，
// 后者属于"真的没号"，绝不能冒充模型问题。
func TestModelBlockedEmptyInputs(t *testing.T) {
	p := New("")
	// 空池
	if st := p.ModelBlocked("glm-5.3"); st.Blocked {
		t.Fatalf("空池不应判定为模型阻塞：%+v", st)
	}
	// 空模型名
	p.Add(&auth.Auth{UID: "u1"})
	if st := p.ModelBlocked(""); st.Blocked {
		t.Fatalf("空模型名不应判定为模型阻塞：%+v", st)
	}
}

// TestModelBlockedThenClear 回归：BlockModelBackoff 写入后应判定为阻塞，
// BlockModelClear 清除后应恢复——覆盖真实的写入/清除通道，而不是只手搓 map。
func TestModelBlockedThenClear(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})

	p.BlockModelBackoff("u1", "glm-5.3", "11102 model [glm-5.3] service info not found")
	st := p.ModelBlocked("glm-5.3")
	if !st.Blocked || st.Count != 1 {
		t.Fatalf("写入负缓存后应阻塞：%+v", st)
	}
	if st.Reason == "" {
		t.Errorf("应带出 reason")
	}

	p.BlockModelClear("u1", "glm-5.3")
	if st := p.ModelBlocked("glm-5.3"); st.Blocked {
		t.Fatalf("清除后不应再阻塞：%+v", st)
	}
}

// TestModelBlockedPausedDoesNotMaskBlock 暂停号不构成「能服务」的证明：
// 除暂停号外全部模型冷却时仍应判定阻塞（合并 #113 时补的口径——
// 否则一个暂停的健康号会掩盖「其余号全被模型级冷却挡住」）。
func TestModelBlockedPausedDoesNotMaskBlock(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3": {Until: time.Now().Add(time.Hour), Reason: "11102 ..."},
	}
	p.byUID["u2"].paused = true // 唯一「健康」的号在暂停让位
	p.mu.Unlock()

	st := p.ModelBlocked("glm-5.3")
	if !st.Blocked {
		t.Fatalf("暂停号不得掩盖模型级阻塞：%+v", st)
	}
	if st.Count != 1 {
		t.Errorf("Count=%d want 1（暂停号不计入）", st.Count)
	}
}

// TestModelBlockTTLShortEnoughToSelfHeal 11102 负缓存必须短到能自愈。
//
// 原参数 6h 起、封顶 24h，把一次偶发误判（11102 不总是「这号真没这模型」——路径级
// 差异、上游瞬时抖动、分类误判都会吐同一个码）放大成 (账号,模型) 锁死整天，多号轮
// 一圈就是整池该模型 503（ithtelab/workbuddy-manager#312 的 0033 / issue #308：实测
// 同一账号两分钟就恢复）。退避本身保留，只是基数与封顶缩短。
//
// 断言用**区间**而不是等值：将来微调（如 3m/12m）不该红，只有回到小时级才必须红。
func TestModelBlockTTLShortEnoughToSelfHeal(t *testing.T) {
	if modelBlockBaseTTL > 5*time.Minute {
		t.Fatalf("modelBlockBaseTTL=%v 过长：一次误判会把 (账号,模型) 锁太久", modelBlockBaseTTL)
	}
	if modelBlockMaxTTL > 15*time.Minute {
		t.Fatalf("modelBlockMaxTTL=%v 过长：反复命中也该在十几分钟内自愈", modelBlockMaxTTL)
	}
	if modelBlockMaxTTL < modelBlockBaseTTL {
		t.Fatalf("封顶 %v 小于基数 %v", modelBlockMaxTTL, modelBlockBaseTTL)
	}
	// 退避仍需存在：base × 2^shift 必须真的超过封顶，否则 Hits 机制形同虚设
	//（阶梯永远到不了 max，指数退避只剩前一两档）。
	if modelBlockBaseTTL<<modelBlockShift < modelBlockMaxTTL {
		t.Fatalf("退避到不了封顶：base<<%d = %v < max %v",
			modelBlockShift, modelBlockBaseTTL<<modelBlockShift, modelBlockMaxTTL)
	}
}
