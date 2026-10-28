// 冷却与熔断：Cooldown（固定时长账号级冷却）、CooldownSoftRate（账号级软冷却，对齐
// 上游重置时间或有界退避）、CooldownSoftForModel（模型级软冷却，对齐重置墙钟）、
// BlockModelBackoff/Clear（11102 负缓存）、软冷却封顶、熔断失败累计、签到解冻。
package pool

import (
	"log"
	"strings"
	"time"
)

func (p *Pool) SetCredits(uid string, credits, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		e.credits = credits
		e.creditsTotal = total
		p.dirty.Store(true)
	}
}

// SetNickname 更新账号昵称并回写 auths 凭证文件（issue #94：上游改名后同步）。
// 昵称未变化时不写盘；uid 不存在 / 昵称为空返回 false。与 token 刷新共用
// auth 自身的锁与 SaveAtomic 原子写，无半更新窗口。
func (p *Pool) SetNickname(uid, nickname string) bool {
	if uid == "" || nickname == "" {
		return false
	}
	p.mu.RLock()
	e, ok := p.byUID[uid]
	p.mu.RUnlock()
	if !ok {
		return false
	}
	a := e.a
	a.Lock()
	changed := a.Nickname != nickname
	if changed {
		a.Nickname = nickname
	}
	a.Unlock()
	if !changed {
		return false
	}
	if err := a.SaveAtomic(); err != nil {
		log.Printf("WARN: [pool] nickname save %s: %v", uid, err)
		return false
	}
	return true
}

// NoteCheckinDone 标记账号今日已签到（签到成功与上游"今天已签到"幂等拒绝均算）。
// 记录本地日期，跨零点自然过期；不触碰冷却/禁用状态（签到与冷却域正交）。
func (p *Pool) NoteCheckinDone(uid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		day := time.Now().Format("2006-01-02")
		if e.lastCheckinDay != day {
			e.lastCheckinDay = day
			p.dirty.Store(true)
		}
	}
}

// SetCreditsDetailed 更新账号余额/总额、配置窗口内的快过架子集，以及最早未来
// 到期批次。earliestAt 为零或不在未来时清空最早批次；expiring/earliestRemaining
// 均钳到 [0, credits]，避免上游脏数据污染选号。
func (p *Pool) SetCreditsDetailed(uid string, credits, total, expiring int64, earliestAt time.Time, earliestRemaining int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		if credits < 0 {
			credits = 0
		}
		if expiring < 0 {
			expiring = 0
		}
		if expiring > credits {
			expiring = credits
		}
		now := time.Now()
		if earliestRemaining < 0 {
			earliestRemaining = 0
		}
		if earliestRemaining > credits {
			earliestRemaining = credits
		}
		if earliestAt.IsZero() || !earliestAt.After(now) || earliestRemaining == 0 {
			earliestAt = time.Time{}
			earliestRemaining = 0
		}
		e.credits = credits
		e.creditsTotal = total
		e.creditsExpiring = expiring
		e.creditsEarliestExpiry = earliestAt
		e.creditsEarliestRemaining = earliestRemaining
		p.dirty.Store(true)
	}
}

// ClearExpiringSnapshots 清空所有账号的快过期/最早到期缓存。配置窗口改变时调用，
// 避免在新快照写入前继续使用旧窗口得到的路由数据；下一次签到或余额刷新会重建。
func (p *Pool) ClearExpiringSnapshots() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byUID {
		e.creditsExpiring = 0
		e.creditsEarliestExpiry = time.Time{}
		e.creditsEarliestRemaining = 0
	}
	p.dirty.Store(true)
}

// Cooldown 冷却账号至 now+d（即时冷却：CoolHard 余额耗尽 / CoolSoft 固定短冷却）。
//
// 重构后本入口是「固定时长的账号级冷却」，不再做两件旧事：
//   - 不再喂熔断器失败计数：熔断器只对「反复失败」（NoteError，5xx）退避。
//     软限流/余额耗尽各有权威恢复时刻（重置墙钟 / 04:00 签到），再并入"连续失败"
//     会让用户正常重试越堆越厚。熔断语义由 NoteError 唯一驱动（与 until 正交保持）。
//   - 不再做 softStreak 指数堆加：固定 d 即最终时长。CoolSoft 的精确对齐请用
//     CooldownSoftRate（有界、对齐上游重置时间）。
func (p *Pool) Cooldown(uid string, kind CoolKind, d time.Duration, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		e.until = time.Now().Add(d)
		e.coolKind = kind
		e.reason = reason
		// 非模型级冷却入口：清空会参与路由的模型级冷却，避免上一次
		// 模型豁免泄漏到账号级冷却上；AuditOnly 条目不影响路由，保留展示。
		clearRoutingModelCooldownsLocked(e)
		p.dirty.Store(true)
	}
}

// CooldownSoftForModel 429 的**模型级**软冷却入口（issue #31）：只写
// modelCooldowns[model]，**绝不写账号级 until**——同号换个模型立即可用。
//
//   - resetAt 非零（上游带「将在 … 重置」）→ Until = 上游墙钟，**不做 soft_rate_max
//     截短**：截短只会在上游恢复前再撞一次同样的 429，白烧一次请求与一次风控信号。
//     仅对明显脏值兜底（已过期按立即恢复、超 7 天按 7 天钳制，见 modelSoftUntilLocked）。
//   - resetAt 零值（无重置时间）→ **模型级有界退避**：base 起按该模型命中次数翻倍、
//     封顶 soft_rate_max。旧实现把它退化成账号级冷却（单模型限流连坐整号），
//     与「6004 是模型级信号」的事实不符，也让「换个模型继续用」彻底失效。
//   - 冷却中重复命中不推进阶梯（铁律：越重试越冷是历史事故的成因）。
//
// model 为空时无法归到 (账号, 模型)：退回账号级有界退避，否则这条限流信号会整个丢掉，
// 账号仍会被反复选中去打同一个 429。
func (p *Pool) CooldownSoftForModel(uid string, base time.Duration, resetAt time.Time, model, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	now := time.Now()
	if model == "" {
		if e.coolKind != CoolSoft || !now.Before(e.until) {
			e.softStreak++
			e.until = now.Add(p.softDurationLocked(base, e.softStreak))
		}
		e.coolKind = CoolSoft
		e.reason = reason
		clearRoutingModelCooldownsLocked(e)
		p.dirty.Store(true)
		return
	}
	if e.modelCooldowns == nil {
		e.modelCooldowns = map[string]modelCooldown{}
	}
	cur := e.modelCooldowns[model]
	if !resetAt.IsZero() {
		e.modelCooldowns[model] = modelCooldown{
			Until:   modelSoftUntilLocked(now, resetAt),
			ResetAt: resetAt,
			Reason:  reason,
			Hits:    cur.Hits + 1,
		}
		p.dirty.Store(true)
		return
	}
	if !cur.AuditOnly && cur.Until.After(now) {
		// 已在模型级冷却中：不推进阶梯、不延长期限，只把最新文案留在台账上。
		e.modelCooldowns[model] = modelCooldown{
			Until:  cur.Until,
			Reason: reason,
			Hits:   cur.Hits,
		}
		p.dirty.Store(true)
		return
	}
	// 阶梯档位用该 (账号, 模型) 的命中次数：Hits 不落盘（重启后从基数重学），
	// 与 11102 负缓存同口径——避免一次进程内计数把模型永久锁在高档。
	hits := cur.Hits + 1
	e.modelCooldowns[model] = modelCooldown{
		Until:  now.Add(p.softDurationLocked(base, hits)),
		Reason: reason,
		Hits:   hits,
	}
	p.dirty.Store(true)
}

// maxModelSoftReset 模型级「明确重置时刻」的脏值上限：上游若给出一个远超合理范围的
// 重置时间（文案解析错、脏数据），按 7 天钳制，避免一个模型被停到遥不可及的未来。
const maxModelSoftReset = 7 * 24 * time.Hour

// modelSoftUntilLocked 计算模型级冷却截止时刻。
//
// 与账号级的 cappedSoftUntilLocked 刻意不同：账号级封顶到 soft_rate_max 是为了让
// 账号尽快回池试一次；模型级的上游墙钟是**唯一权威恢复时刻**，截短等于在上游恢复前
// 再送一次必然失败的请求。这里只兜两种明显脏值。
// 调用方必须已持有 p.mu。
func modelSoftUntilLocked(now, resetAt time.Time) time.Time {
	if !resetAt.After(now) {
		// 时钟偏移或文案已过期：立即恢复（不把模型白锁一段时间）。
		return now.Add(time.Millisecond)
	}
	if limit := now.Add(maxModelSoftReset); resetAt.After(limit) {
		return limit
	}
	return resetAt
}

// modelBusyBase/modelBusyMax 14003「模型繁忙」的独立停车窗口。
//
// 不复用 soft_rate 家族：那套阶梯（默认 600s 起、按命中翻倍、封顶 2h）是给
// 「这个账号/模型被持续限流」准备的，惩罚一次瞬时抖动代价过高。14003 的文案就是
// 「模型繁忙，请换模型或稍后重试」——它是**间歇**的（生产日志里同一分钟内既有
// 14003 也有成功），所以首档取 10s 而不是分钟级。同一 (号, 模型) 反复命中再翻倍，
// 封顶 1 分钟——宁可多交一次 429，也不要把间歇繁忙的模型停成整分钟不可用。
const (
	modelBusyBase = 10 * time.Second
	modelBusyMax  = time.Minute
)

// CooldownModelBusy 把 (账号, 模型) 停一个短窗口（14003 模型繁忙专用）。
//
//   - 只写 modelCooldowns[model]，不碰账号级 until：同号换模型立即可用。
//   - 同一窗口内重复命中不延长（并发请求/兜底探测不得把 10s 堆成分钟级）。
//   - 已有更晚的冷却（6004 墙钟 / 11102 负缓存）时保留原截止，不缩短。
func (p *Pool) CooldownModelBusy(uid, model, reason string) {
	p.CooldownModelBusyWithWait(uid, model, reason, 0)
}

// CooldownModelBusyWithWait 同 CooldownModelBusy，但上游给了 Retry-After 时采信它：
// 14003 的「稍后重试」有权威等待时长时，按它停车比猜 10s 更准。
//
// wait 仍钳到 (0, modelBusyMax]：这个头也可能来自边缘/WAF，超长值会把一个间歇繁忙
// 的模型停成不可用——1 分钟的既有上限是这条路径的硬边界。
func (p *Pool) CooldownModelBusyWithWait(uid, model, reason string, wait time.Duration) {
	if uid == "" || model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	now := time.Now()
	if e.modelCooldowns == nil {
		e.modelCooldowns = map[string]modelCooldown{}
	}
	cur := e.modelCooldowns[model]
	if wait > 0 {
		if wait > modelBusyMax {
			wait = modelBusyMax
		}
		// 权威等待时长优先，但不缩短已有的更晚截止（6004 墙钟 / 11102 负缓存）。
		until := now.Add(wait)
		if cur.Until.After(until) {
			until = cur.Until
		}
		e.modelCooldowns[model] = modelCooldown{Until: until, Reason: reason, Hits: cur.Hits + 1}
		p.dirty.Store(true)
		return
	}
	if cur.Until.After(now) {
		return
	}
	hits := cur.Hits + 1
	d := modelBusyBase
	for i := 1; i < hits && d < modelBusyMax; i++ {
		d *= 2
	}
	if d > modelBusyMax {
		d = modelBusyMax
	}
	e.modelCooldowns[model] = modelCooldown{Until: now.Add(d), Reason: reason, Hits: hits}
	p.dirty.Store(true)
}

// RecordModelRateLimitAudit 记录一条**只展示不参与路由**的模型限流台账。
//
// 6004 无重置时间的路径已不再使用它：那条路径现在写真实的模型级冷却
// （CooldownSoftForModel 的零 resetAt 分支），因为「模型被限流」本来就该只停该模型。
// 本方法保留给「确实只想在账号页留一行、不得影响 healthyForModel」的场合：
// AuditOnly 条目被 modelCooled 忽略，因此不会改动选号行为。
func (p *Pool) RecordModelRateLimitAudit(uid, model, reason string) {
	if uid == "" || model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	now := time.Now()
	if old, exists := e.modelCooldowns[model]; exists && !old.AuditOnly && old.Until.After(now) {
		return // 已有真实模型冷却，审计记录不得覆盖路由截止
	}
	until := e.until
	if until.IsZero() || !until.After(now) {
		until = now.Add(p.softRateMaxOr())
	}
	if e.modelCooldowns == nil {
		e.modelCooldowns = make(map[string]modelCooldown)
	}
	e.modelCooldowns[model] = modelCooldown{
		Until:     until,
		Reason:    reason,
		AuditOnly: true,
	}
	p.dirty.Store(true)
}

// clearRoutingModelCooldownsLocked 删除参与选号豁免的模型冷却，保留 AuditOnly 台账。
// 调用方必须已持有 p.mu 写锁。
func clearRoutingModelCooldownsLocked(e *entry) {
	for model, mc := range e.modelCooldowns {
		if !mc.AuditOnly {
			delete(e.modelCooldowns, model)
		}
	}
	if len(e.modelCooldowns) == 0 {
		e.modelCooldowns = nil
	}
}

// modelBlock TTL 常量（11102 负缓存退避）：
// 首次命中 6h；半开到期后允许放行重试，再次命中 TTL = base × 2^min(hits-1, shift)；
// 封顶 24h（最多一天再试一次）。该模型请求成功即由 BlockModelClear 清除。
const (
	modelBlockBaseTTL = 6 * time.Hour
	modelBlockShift   = 4
	modelBlockMaxTTL  = 24 * time.Hour
)

// BlockModelBackoff 11102「该后端无此模型」的 (账号, 模型) 负缓存入口
// （handler.applyErrorPolicy 调用）。复用 modelCooldowns 机制（不新建平行状态）：
// 写 modelCooldowns[model]，Until 为指数退避 TTL，选号侧 healthyForModel 自动对该
// 账号避开该模型。
//
// 语义与 6004 正交：6004 是「模型被限流、对齐重置墙钟」，本入口是「官方确定该后端
// 无此模型、重试无意义，只能换模型/换账号」。resetAt 无需传（11102 无重置文案），
// ResetAt 保持零值，与 6004 台账共用 Until 判定——11102 条目会以 11102 reason 出现在
// /status 台账，运维可见。
func (p *Pool) BlockModelBackoff(uid, model, reason string) {
	if uid == "" || model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	now := time.Now()
	hits := 0
	if e.modelCooldowns != nil {
		hits = e.modelCooldowns[model].Hits
	}
	hits++
	ttl := modelBlockBaseTTL
	if d := ttl * (1 << uint(min(hits-1, modelBlockShift))); d < modelBlockMaxTTL {
		ttl = d
	} else {
		ttl = modelBlockMaxTTL
	}
	if e.modelCooldowns == nil {
		e.modelCooldowns = map[string]modelCooldown{}
	}
	e.modelCooldowns[model] = modelCooldown{
		Until:  now.Add(ttl),
		Reason: reason,
		Hits:   hits,
	}
	p.dirty.Store(true)
}

// BlockModelClear 清除 (账号, 模型) 的 11102 负缓存条目（该模型实测又通了）。半开探测
// 或正常请求对该模型成功后调用（handler 成功路径）。只清 11102 条目、不碰 6004 独立
// 冷却表——6004 有自身上游重置墙钟语义，成功不该抹掉。reason 前缀判定区分两者：
// 11102 条目的 reason 恒以 "11102" 开头（见 upstream.BlockModelReason）。
func (p *Pool) BlockModelClear(uid, model string) {
	if uid == "" || model == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok || len(e.modelCooldowns) == 0 {
		return
	}
	mc, exists := e.modelCooldowns[model]
	// 11102「该后端无此模型」/ 11158「该账号类型无权使用」都是确定性负缓存：
	// 权限或路由恢复后，成功请求必须能清掉，否则模型会被永久锁在本账号上。
	if !exists || (!strings.HasPrefix(mc.Reason, "11102") && !strings.HasPrefix(mc.Reason, "11158")) {
		return
	}
	delete(e.modelCooldowns, model)
	if len(e.modelCooldowns) == 0 {
		e.modelCooldowns = nil
	}
	p.dirty.Store(true)
}

// ModelBlockStatus 描述某模型在全池范围内因模型级冷却而不可选的情况。
type ModelBlockStatus struct {
	Blocked bool      // true = 每个非禁用账号都对该模型处于冷却中
	Reason  string    // 冷却原因（通常为上游原文，如 "11102 model ... not found"）
	Until   time.Time // 最早解封时间（零值 = 上游未给重置时刻）
	Count   int       // 因此被挡的账号数
}

// ModelBlocked 报告该模型是否在**全池**范围内被模型级冷却挡住。
// 等价于 ModelBlockedForRealm("", model)：只用于没有 realm 上下文的调用方。
func (p *Pool) ModelBlocked(model string) ModelBlockStatus {
	return p.ModelBlockedForRealm("", model)
}

// ModelBlockedForRealm 报告该模型在**指定域**内是否被模型级冷却挡住。
//
// 为什么要分域（issue #102 附带发现 1 的补足）：选号是按 realm 过滤的
// （PickExcludingForRealm），而判定此前一直按全池统计——另一域只要有健康号，
// 本域「全被 6004/11102 挡住」就会被判成「不是模型级阻塞」，于是客户端拿到
// no_healthy_account（"号全挂了"），而真相是"这个域里所有号对这个模型都停了"。
// 判定口径必须与选号口径一致，否则报错文案指错方向。
//
// realm 为空 = 全池（既有语义：调用方确实试过所有域时才是这个口径）。
func (p *Pool) ModelBlockedForRealm(realm, model string) ModelBlockStatus {
	if model == "" {
		return ModelBlockStatus{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()

	var st ModelBlockStatus
	for _, e := range p.byUID {
		// 暂停选号（paused）号与禁用号同口径跳过：它此刻不可选，「能服务」的
		// 证明不成立——否则一个暂停的健康号会掩盖「其余号全被模型级冷却挡住」。
		if e.disabled || e.paused {
			continue // 禁用/暂停号不参与：它们的不可用与模型无关
		}
		// 域过滤与选号一致（e.a.Realm() 的取值域是 "cn"/"global"）。
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if !e.modelCooled(now, model) {
			// 本域还有账号能服务这个模型 → 不是模型级阻塞。
			return ModelBlockStatus{}
		}
		st.Count++
		if mc, ok := e.modelCooldowns[model]; ok {
			if st.Reason == "" {
				st.Reason = mc.Reason
			}
			// 取最早解封：那才是"再等多久值得重试"的答案。
			if !mc.Until.IsZero() && (st.Until.IsZero() || mc.Until.Before(st.Until)) {
				st.Until = mc.Until
			}
		}
	}
	if st.Count == 0 {
		// 该域没有可参与判定的账号：这是"真的没号"，不是模型问题。
		return ModelBlockStatus{}
	}
	st.Blocked = true
	return st
}

// CooldownSoftRate 429/限流文案的**账号级**软冷却入口（handler.applyErrorPolicy 调用）。
//
// 语义：
//   - resetAt 非零（上游带权威重置时间，无论 6004 还是 11140 rate-limiting）→
//     账号级直到该墙钟（截断到 softRateMax，绝不指数堆加）；**不**在
//     modelCooldowns 记模型（账号级语义，不产生切模型豁免——普通账号级限流不该
//     因切模型绕过）。
//   - resetAt 零值且**不在冷却中**（首次/恢复后的新限流）→ 有界退避：按 softStreak
//     指数退避并封顶 softRateMax。softStreak 只在真正进入一次新冷却时计数，由
//     NoteSuccess/reviveCoolingLocked 清零（既有恢复语义）。
//   - resetAt 零值且**已在软冷却中**（兜底探测再次撞 429）→ 不推进 streak、不延长
//     until：用户重试/并发兜底探测不得把冷却越堆越厚——这正是旧实现「越重试越冷、
//     全池被推到 2h 封顶」的元凶（每次探测都 softStreak++ 指数翻倍）。
func (p *Pool) CooldownSoftRate(uid string, base time.Duration, resetAt time.Time, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		now := time.Now()
		if !resetAt.IsZero() {
			e.until = p.cappedSoftUntilLocked(now, resetAt)
		} else if e.coolKind != CoolSoft || !now.Before(e.until) {
			// 新限流（不在有效软冷却中）：推进有界退避；兜底探测（仍在软冷却中）不翻倍。
			d := p.softDurationLocked(base, e.softStreak+1)
			e.softStreak++
			e.until = now.Add(d)
		}
		e.coolKind = CoolSoft
		e.reason = reason
		clearRoutingModelCooldownsLocked(e) // 账号级软冷却：清路由豁免，保留审计台账
		p.dirty.Store(true)
	}
}

// cappedSoftUntilLocked 把上游重置墙钟截断到 softRateMax（now+softRateMax 与 resetAt
// 取较早者）。resetAt 已过期（时钟偏移/文案过期）时时长钳到时间零点附近，立即恢复。
// 调用方必须已持有 p.mu。
func (p *Pool) cappedSoftUntilLocked(now, resetAt time.Time) time.Time {
	cap := now.Add(p.softRateMaxOr())
	if resetAt.After(cap) {
		return cap
	}
	if resetAt.After(now) {
		return resetAt
	}
	return now.Add(time.Millisecond)
}

// softRateMaxOr 返回生效的 softRateMax（未注入时按默认 2h），供封顶计算。
// 调用方必须已持有 p.mu。
func (p *Pool) softRateMaxOr() time.Duration {
	if p.softRateMax > 0 {
		return p.softRateMax
	}
	return defaultSoftRateMax
}

// softDurationLocked 按连续软冷却次数把基数 d 指数放大：d << (streak-1)，封顶 softRateMax。
// softRateMax 未注入（<=0）时按 defaultSoftRateMax 算。streak<=1 时原样返回 d。
// 左移位数受 softStreakShiftMax 限制，避免 streak 极大时移位溢出。
// 调用方必须已持有 p.mu。
func (p *Pool) softDurationLocked(d time.Duration, streak int) time.Duration {
	if streak <= 1 {
		return d
	}
	shift := streak - 1
	if shift > softStreakShiftMax {
		shift = softStreakShiftMax
	}
	d <<= shift
	max := p.softRateMax
	if max <= 0 {
		max = defaultSoftRateMax
	}
	if d > max || d <= 0 { // d<=0：左移溢出成负数/零，同样按封顶兜底
		d = max
	}
	return d
}

// recordBreakerFailureLocked 累计一次熔断失败；达到阈值则按指数退避熔断。
// 熔断与冷却（until）解耦：冷却按错误类别给固定时长，熔断则对"反复失败"逐次加长封禁。
// 调用方必须已持有 p.mu。
func (p *Pool) recordBreakerFailureLocked(e *entry) {
	e.fails++
	if e.fails < p.breakerThreshold {
		return
	}
	d := p.breakerCooldown
	for i := 0; i < e.retryCount; i++ {
		d *= 2
		if d >= p.breakerCooldownMax {
			d = p.breakerCooldownMax
			break
		}
	}
	// 触发熔断：重置失败计数供下一轮重新累计；retryCount 递增放大退避指数。
	e.fails = 0
	e.retryCount++
	e.breakerUntil = time.Now().Add(d)
}

// CooldownUntilTomorrow4AM 冷却到下一个 04:00（本地时区）。
// 用于 ErrHardCredit 场景：积分耗尽账号等签到任务（09:00/21:00）恢复。
func (p *Pool) CooldownUntilTomorrow4AM(uid string, reason string) {
	now := time.Now()
	p.Cooldown(uid, CoolHard, nextDay4AM(now).Sub(now), reason)
}

// nextDay4AM 返回 now 之后最近的一个 04:00（与 now 同一时区）。
// now 在当天 04:00 之前（凌晨 00:00~04:00）时返回当天 04:00——此时签到尚未执行，
// 该窗内触发的硬冷却等当天签到即可恢复；返回次日会白冷约一天。
// 04:00 整及之后返回次日 04:00。
// time.Date 对日溢出自动进位（月末→下月 1 号、年末→下年 1 号），天然覆盖跨日/跨月/跨年。
func nextDay4AM(now time.Time) time.Time {
	if now.Hour() < 4 {
		return time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
	}
	return time.Date(now.Year(), now.Month(), now.Day()+1, 4, 0, 0, 0, now.Location())
}

// ReenableIfCredits 签到/余额刷新后解冻：仅当 remain > 0 且账号非禁用时，解冻
// **余额耗尽冷却**（CoolHard）。软限流（CoolSoft）与模型级台账（modelCooldowns）
// 不在此清除——它们的恢复证据是上游重置墙钟到期，不是余额恢复（余额刷新周期
// 任务每 5 分钟到达这里，全清会把限流冷却实际寿命压到一个刷新周期内）。
// 注意：不碰熔断器——熔断到期（breakerUntil 过期）或下次 chat 成功（NoteSuccess）才恢复。
// reviveCoolingLocked 已迁至 transition.go（状态机迁移唯一权威实现）。
