# Research: 上游「6020/6021 渠道排队语义」+「11102 负缓存 TTL」两处改动档案

- Query: 提取上游 `f07eba5`（含 merge `80e844f`）的 6020/6021 排队语义改动、`4f79fc8` 中 11102 负缓存 TTL 收窄改动，产出可照着移植的技术档案
- Scope: mixed（上游仓库离线对象 `upstream/main` + 本仓 `main` 分叉对照）
- Date: 2026-10-28

对照坐标：
- 上游前一提交 = `f07eba5~1` = `47f1ed6`；本仓上次同步基线 = `5367e78`
- `80e844f` 是 merge 提交（第一父 `47f1ed6`、第二父 `f07eba5`）。**实测 `git diff f07eba5 80e844f -- internal/server/handler.go internal/upstream/client.go internal/server/handler_test.go internal/upstream/client_test.go` 为空**：merge 没有再改代码，内容 100% 等于 `f07eba5`。下文所有「上游」行号均指 `upstream/main`。

---

## 0. 结论速览（先看这段）

1. **两处改动都很小**：`f07eba5` 只动 4 个文件、+157/−47（`internal/server/handler.go`、`internal/server/handler_test.go`、`internal/upstream/client.go`、`internal/upstream/client_test.go`），`hint.go` **完全没动**。`4f79fc8` 的 pool 部分只动 3 个文件（`cooldown.go` 常量+注释、`entry.go` 两处注释、`modelblocked_test.go` 注释+1 个新守卫用例）。
2. **核心逻辑三层**：① `upstream/client.go` 新增 `parseQueueRetryAfter`，从 6020 body 的 `data.retry_after` 抽等待口径，挂到 `*upstream.Error.RetryAfter`；② `handler.go` 轮转循环里 `ErrUpstreamBusy` 由 fail-fast 改成 `fail() + rotateBackoff() + continue`；③ 末端 `switch ue.Kind` 新增 `case ErrUpstreamBusy`：**503 → 429 + Retry-After**。
3. **必须注意的隐性结构改动**：上游把 `retryAfterSec := 0` 的声明**从 switch 之后提前到了 `errors.As` 之前**（上游 :1748）。不提前，case 里无法写 `retryAfterSec`。
4. **不罚号原则一字未改**：`applyErrorPolicy` 的 `ErrUpstreamBusy` 分支仍是零动作（不冷却/不熔断/不 `NoteError`/不喂连败）。
5. **封顶是两级**：body 侧 `parseQueueRetryAfter` 先按 `retryAfterSanity = 2h` 截断；末端上报再按 `maxRetryAfterSec = 600` 截断；两级都拿不到 → 本地兜底 `5`。
6. **6000 与 6020/6021 是同一状态码（429）下的两套相反策略**：6000 = 模型级用量配额 → 429 + **不发** Retry-After（等到重置前都无效）；6020/6021 = 渠道排队 → 429 + **发** Retry-After（等就有效）。语义分界写在 `client.go:246-250`、`client.go:669-678`、`handler.go:1828-1844` 三处注释里。
7. **11102 负缓存**：`6h/24h → 2m/10m`，退避曲线不变（`base × 2^min(hits-1, 4)`，封顶 `max`）；之所以不改成「每请求重试」，是因为真没模型的号会变成每个请求都白打一次上游。
8. **本仓（`linguo2625469` 分叉）与上游的差异是结构性的**，不能照抄 diff：
   - 本仓末端 `switch ue.Kind` **完全没有** `case upstream.ErrUpstreamBusy`（也没有 `ErrHardCredit` 分支）——因为 fail-fast 让它不可达；
   - 本仓 `hint.go` 文案与上游不同（上游 :66 / 本仓 :67）；
   - 本仓没有 `math` 导入（上游用 `math.Ceil`，本仓既有代码用 `int(d.Seconds())+1`）；
   - 本仓测试文件名不同（`handler_apilayer_test.go` vs 上游 `handler_test.go`），断言也不同。
9. **上游留了一处过期注释**：`upstream/main:internal/upstream/client.go:50` 的 `ErrUpstreamBusy` 行尾仍写「不轮转，末端 503 透传原文」——`f07eba5` 忘了同步。移植时**不要照抄这句**。

---

## 1. `parseQueueRetryAfter`：上游新增函数（全文）

文件：`internal/upstream/client.go`，位置：`upstream/main:498-520`（紧跟 `ParseRetryAfter` 之后、`isAllDigits` 之前；本仓对应插入点是 `internal/upstream/client.go:477` 之后）。

```go
// parseQueueRetryAfter 从 6020/6021 排队响应的 body 抽上游明示的重试等待
// （data.retry_after，整数秒）。上游把排队口径放在 body 而不是 Retry-After 头
// （2026-10-28 真机：429 + code 6020，data 含 queue_position/queue_size/
// retry_after/estimated_wait，头侧无任何等待信息），ParseRetryAfter（只认头）
// 拿不到。只认结构化 data 字段——msg 是 i18n 键（queue.waiting.title）无数值
// 可解析，requestId 等恰好含数字的字段不误伤。超过 retryAfterSanity 按异常值
// 封顶（estimated_wait 是另一个字段，不会误入；防上游口径漂移时把天文数字带回）。
// 解析不到返回 false，调用方回落本地兜底。
func parseQueueRetryAfter(body []byte) (time.Duration, bool) {
	var env struct {
		Data struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Data.RetryAfter <= 0 {
		return 0, false
	}
	d := time.Duration(env.Data.RetryAfter) * time.Second
	if d > retryAfterSanity {
		d = retryAfterSanity
	}
	return d, true
}
```

要点：

| 项 | 值 / 说明 |
|---|---|
| 取值路径 | `body.data.retry_after`（int64，整数秒）。只解这一层结构体，不做正则/全文扫数字 |
| 缺失 / 非法 / `<= 0` | 返回 `(0, false)` → 调用方回落本地兜底（末端 `5s`） |
| 封顶常量 | `retryAfterSanity = 2 * time.Hour`（`client.go:470`，本仓 `client.go:451` 已有同值同义常量，**无需新增**） |
| 为什么不认头 | 真机 `429 + code 6020` 的响应头**没有任何等待信息**；`ParseRetryAfter`（只认 `Retry-After` / `Retry-After-Ms` / `X-Ratelimit-Reset`）拿不到 |
| 为什么不认 msg | `msg` 是 i18n 键 `queue.waiting.title`，无数值可解析；`requestId` 里恰好有数字，不能全文匹配 |
| 为什么不认 `estimated_wait` | 是另一个字段（真机 `12510`，量级明显不像秒级等待口径），显式只认 `retry_after` |

### 1.1 挂载点：`ChatStreamContext` 的错误信封

文件：`internal/upstream/client.go`，上游 `upstream/main:1588-1600`；本仓对应位置 `internal/upstream/client.go:1294-1298`（本仓现状只有头解析）。

上游改后的形状（`git diff f07eba5~1 f07eba5` 原样）：

```go
 			ue := &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
 			if d, ok := ParseRetryAfter(resp.Header); ok {
 				ue.RetryAfter = d
+			} else if kind == ErrUpstreamBusy {
+				// 6020 排队信封的等待口径在 body（data.retry_after），不在头
+				//（见 parseQueueRetryAfter 注释）。头解析拿不到时从 body 抽，
+				// 供 handler 末端 Retry-After 上报使用。
+				if d, ok := parseQueueRetryAfter(raw); ok {
+					ue.RetryAfter = d
+				}
 			}
 			return nil, resp.StatusCode, raw, ue
```

**字段名：`RetryAfter`，类型 `time.Duration`，挂在 `*upstream.Error` 信封上**（`client.go:94-102` 定义，注释原文：「Kind 决定『罚不罚』，RetryAfter 决定『罚多久』，同为上游响应的一等公民」）。头解析优先、body 是 `else if` 回落——头若给了口径就以头为准。

注意 `else if kind == ErrUpstreamBusy` 这个守卫：`raw` 在函数里已被 `truncate` 用于 `Msg`（截 200 字符），但 `parseQueueRetryAfter(raw)` 用的是**完整 raw**，所以 JSON 被截断也不会影响解析——不过若上游 body 巨大且 `data` 在后部，这里仍用完整原文，安全。

---

## 2. 轮转恢复：`handler.go` 里 `ErrUpstreamBusy` 由 fail-fast 改成换号重试

### 2.1 上游 diff（函数 `chatCompletions`，轮转循环 `for i := 0; i < h.cfg.MaxRotate; i++`，`handler.go:1150`）

改动前（= `5367e78`，本仓现状一致，本仓 `handler.go:1459-1480`）：

```go
			// 6021「queue.full.title」渠道级容量满载：不罚号不轮转，立即透传上游原文
			//（503 + upstream_busy，含 requestId）。上游集群排队打满时换任何账号都撞
			// 同一堵墙——按限流处置会逐号软冷却、与全冷却兜底互相喂成整池停摆
			//（2026-10-15 实案：18 次全落 soft_rate），且换号把无效请求放大
			// MaxRotate 倍。客户端按 SDK 退避重试即可；Retry-After 是本地兜底
			//（上游未给重置口径时按 5s 提示，PR #180 同款）。
			if kind == upstream.ErrUpstreamBusy {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				msg := string(respBody)
				if strings.TrimSpace(msg) == "" {
					msg = "upstream capacity is full (queue.full); retry after a short wait"
				}
				retryAfter := "5"
				if uerr != nil && uerr.RetryAfter > 0 && int64(uerr.RetryAfter.Seconds()) > 0 {
					retryAfter = strconv.FormatInt(int64(uerr.RetryAfter.Seconds()), 10)
				}
				w.Header().Set("Retry-After", retryAfter)
				writeOpenAIErrorHint(w, http.StatusServiceUnavailable, "upstream_busy", msg,
					h.hintOf(upstream.ErrUpstreamBusy, string(respBody), bareModel, reqHasImage, uerr))
				st.status = http.StatusServiceUnavailable
				st.outcome = reqlog.OutcomeHTTPError
				return
			}
```

改动后（`upstream/main:1421-1447`，代码段自 :1439 起）：

```go
			// 6020「queue.waiting.title」/6021「queue.full.title」渠道级排队/容量满载：
			// **不罚号**（保留 1e70717/b0797d7 的核心修正——上游容量信号与账号无关，
			// 按 ErrSoftRate 罚号会与全冷却兜底互相喂成整池停摆，2026-10-15 实案），
			// 但恢复**请求内轮转**，终态映射见末端 ErrUpstreamBusy 分支（429 + Retry-After）。
			//
			// 为什么轮转回来了：排队可用性是**间歇的**——2026-10-28 生产实案（多号池，
			// kimi-k3.1-pro）同一分钟内既有 6021 也有成功（16:38 两号连续 6021 后 33s
			// 内同模型即恢复），一次性 fail-fast 把几秒的抖动直接交给客户端。队列按
			// client_user_type 全局共享（实测 position 2502→2648，9s 涨 146），换号
			// 通常只是多一次撞墙、无法绕过排队本身——所以这里轮转的价值不在「绕过」，
			// 而在间歇空窗不再逐请求致命；代价是 MaxRotate 次上游调用，客户端按
			// Retry-After 重试的节奏下有界。
			//
			// 为什么终态回 429 而不是 503：上游对这两个形态本身回的就是 429（6020 还
			// 带 data.retry_after=40），网关改写成 503 等于把「稍后再试」翻译成「服务
			// 不可用」——WorkBuddy CLI 对 429 按 Retry-After 自动重试（官方排队语义），
			// 对 503 直接中断任务（2026-10-28 实案：任务在两次 503 后死亡）。透传上游
			// 原状态，让下游用自己的重试策略消化排队。
			if kind == upstream.ErrUpstreamBusy {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				fail(acct.UID)
				lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody), RetryAfter: uerr.RetryAfter}
				if !rotateBackoff(i, r.Context()) {
					break // ctx 取消：终止轮转（排队满载换号退避）
				}
				continue
			}
```

**逐行含义**（这是移植时最需要还原的语义，不只是代码形状）：

| 动作 | 为什么必须保留 |
|---|---|
| `h.applyErrorPolicy(...)` 仍然先调 | 保留「不罚号」的唯一入口：该 kind 在 `applyErrorPolicy` 里是**零动作**分支（见 2.2）。调它是为了让「罚不罚」只有一个决策点，将来改动集中 |
| `fail(acct.UID)` | 释放该号在途租约 + 若失败号是会话粘性号则解绑（本仓 `fail` 定义 `handler.go:1006-1012`）。注意：排队与账号健康无关，这里解绑粘性是上游沿用旧写法的**副作用**（见 §6「需决策」） |
| `lastErr = &upstream.Error{... Msg: string(respBody) ...}` | `uerr.Msg` 在 upstream 侧被 `truncate(...,200)`；末端透传要求**上游原文全量**，所以用完整 `respBody` 重建信封，**同时把 `RetryAfter` 一起搬过去**（否则末端拿不到 body 口径，只剩 5s 兜底） |
| `rotateBackoff(i, ctx)` | 换号前退避，`backoffAfter(i) = 500ms·2^i` 封顶 `8s`，再 ±25% 抖动（`backoff.go:19/24/26`）。返回 `false` 表示 ctx 已取消（客户端断连/优雅停机）→ `break` 终止轮转 |
| `continue` | 进入下一轮换号；`tried[acct.UID]=true` 已在选号时写入（本仓 `handler.go:1195`），所以不会重复选同一号 |

**`MaxRotate` 的作用与取值**：
- 字段：`Config.MaxRotate int // 单请求最多换号次数，默认 3`（上游 `handler.go:37` / 本仓 `handler.go:37`）；`NewHandler` 里 `if cfg.MaxRotate <= 0 { cfg.MaxRotate = 3 }`（上游 `handler.go:186-187` / 本仓 `handler.go:149-150`）。
- 作用：这个分支从「1 次上游调用」变成「最多 `MaxRotate` 次」；池里号数不足 `MaxRotate` 时选号返回 `nil` → `break`，提前走末端。
- 上游注释里明确「代价是 MaxRotate 次上游调用（默认 3，退避 500ms/1s）」。本仓 `MaxRotate` 语义完全一致，**无需改动**。

### 2.2 「不冷却 / 不熔断 / 不喂连败」的实现细节（`applyErrorPolicy`，上游 `handler.go:2280` 起 / 本仓 `handler.go:2159-2161`）

改动前：

```go
 	case upstream.ErrUpstreamBusy:
-		// 6021「queue.full.title」渠道级容量满载：账号余额健康、未限流、session
-		// 未死，换号也撞同一堵墙。零动作（不冷却/不熔断/不 NoteError/不喂连败，
-		// 同 ErrContentBlocked 待遇）——一旦按 ErrSoftRate 罚号，会与全冷却兜底
-		// 互相喂成整池停摆（2026-10-15 实案）。chatCompletions 已 fail-fast 503
-		// 透传原文、终止轮转——该分支只为文档完备。
+		// 6020「queue.waiting.title」/6021「queue.full.title」渠道级排队/容量满载：
+		// 账号余额健康、未限流、session 未死，换号也撞同一堵墙。零动作（不冷却/
+		// 不熔断/不 NoteError/不喂连败，同 ErrContentBlocked 待遇）——一旦按
+		// ErrSoftRate 罚号，会与全冷却兜底互相喂成整池停摆（2026-10-15 实案）。
+		// chatCompletions 轮转耗尽后走末端 ErrUpstreamBusy 分支回 429 + Retry-After，
+		// 不罚号原则在此保持不变。
```

**只有注释变了，分支体（空 case）一个字都没动**。「不喂连败」也自动成立：`NoteFailures` / `NoteError` 只出现在别的分支与传输层错误路径，本分支不触碰。

---

## 3. 末端映射：503 → 429 + `Retry-After`

### 3.1 位置：**不在 `applyErrorPolicy`，也不在 `writeError`** —— 就在 `chatCompletions` 函数体末尾的 `switch ue.Kind`

本仓没有独立的 `writeError`/`writeAllFailed` 函数：末端错误映射写在 `chatCompletions` 内（`handler.go:1681` 起，`switch ue.Kind` 在 `1708`），最终由 `writeOpenAIErrorHint(...)`（`handler.go:1810`）或 `writeModelQuotaError(...)`（`1808`）落盘。`applyErrorPolicy` 是**罚号**策略表（`handler.go:2036`），不是状态码映射表。

上游 diff（`upstream/main:1755-1777`）：

```go
 		case upstream.ErrUpstreamBusy:
-			// 6021 渠道级容量满载（防御路径：正常已在轮转循环内 fail-fast，本分支
-			// 只在异常流经末端时兜底）。503 + 可重试语义，上游原文（下方统一透传）
-			// 含 requestId。
-			status = http.StatusServiceUnavailable
+			// 6020/6021 渠道级排队/容量满载：轮转已耗尽（本请求每个号都撞同一堵墙）。
+			// 回 429 + Retry-After（上游 data.retry_after，缺省 5s，封顶 maxRetryAfterSec）
+			// 而不是 503：上游对这两个形态本身回 429（6020 带 retry_after=40），透传
+			// 原状态让下游按 OpenAI 限流语义重试——WorkBuddy CLI 对 429 自动睡后重发
+			//（官方排队语义），对 503 直接中断任务（2026-10-28 生产实案）。上游原文
+			//（requestId / queue_position）仍在 message 原样透传，供客户端日志定位。
+			status = http.StatusTooManyRequests
 			code = "upstream_busy"
-			msg = "upstream capacity is full (queue.full); retry after a short wait"
+			msg = "upstream queue is full; retry after a short wait"
+			if ue.RetryAfter > 0 {
+				if secs := int(math.Ceil(ue.RetryAfter.Seconds())); secs > 0 {
+					retryAfterSec = secs
+					if retryAfterSec > maxRetryAfterSec {
+						retryAfterSec = maxRetryAfterSec
+					}
+				}
+			}
+			if retryAfterSec == 0 {
+				retryAfterSec = 5 // 上游未给口径时的本地兜底（沿用旧 fail-fast 分支的 5s）
+			}
```

### 3.2 前置条件：`retryAfterSec` 声明必须提前

上游同一 diff 里还有一处**容易漏**的结构改动（`upstream/main:1746-1752`）：

```go
+	// retryAfterSec 非 0 时写 Retry-After 头：只有「全池该模型只是忙/被限流」
+	// 这种可重试形态才有意义（见下方 modelBlock 分支）。
+	retryAfterSec := 0
 	var ue *upstream.Error
 	if errors.As(lastErr, &ue) {
 		hint = h.hintOf(ue.Kind, ue.Msg, bareModel, reqHasImage, ue)
 		switch ue.Kind {
```

本仓现在把 `retryAfterSec := 0` 放在 `switch` **之后**（`handler.go:1747`，位于 `if modelBlock.Blocked` 之前）。移植时必须把它上移到 `var ue *upstream.Error` 之前，否则 case 里写不到这个变量。

### 3.3 `Retry-After` 头的取值优先级

```
1. body 口径：ue.RetryAfter（来自 parseQueueRetryAfter，data.retry_after 整数秒）
   ├─ parseQueueRetryAfter 内部先按 retryAfterSanity = 2h 截断
   └─ 末端再按 maxRetryAfterSec = 600 截断（math.Ceil 向上取整）
2. 无 body 口径（6021 队列满通常就没有 data）→ 本地兜底 5s
3. 封顶：maxRetryAfterSec = 600（const，handler.go:1920 上游 / 1825 本仓，值相同）
4. 最终写头：if retryAfterSec > 0 { w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec)) }
```

实测算术（上游测试 `TestChatQueueWaitingRetryAfterFromBody` 钉住的）：`retry_after=40` → `Retry-After: 40`（不是 5）；`retry_after=999999` → body 侧先截 7200s → 末端再截 600。

注意与 `modelBlockIsTransient`（14003 模型忙）分支的区别：那条走 `int(d.Seconds())+1`（因为 `d = time.Until(Until)` 是非整秒差值，向下取整会提前醒来）。**6020 的 `RetryAfter` 是上游给的整数秒，语义上是「至少睡够」，用 `math.Ceil` 与用 `int()+1` 只差 1s**；上游选了 `math.Ceil`。

### 3.4 上游原文 message 如何透传

这段逻辑与旧路径一致，都在 switch **之后**统一覆盖（上游 `upstream/main:1800-1804` / 本仓 `handler.go:1729-1733`）：

```go
		if s := strings.TrimSpace(ue.Msg); s != "" {
			// 上游原文优先：透传 code/msg/requestId，不拼接本地前缀。
			msg = s
			upstreamMsgPassed = true
		}
```

即：`msg = "upstream queue is full; retry after a short wait"` 只是**没有上游原文时的兜底**；实际响应里 `error.message` 会是 6020/6021 的整段 JSON 原文（含 `code`/`msg`/`requestId`/`data.queue_position`）。所以 `data.retry_after` 也会随着 message 一起被下游看到——客户端既读头也能从 message 里看到原始排队信息。

### 3.5 `upstream/hint.go`：**该 kind 的 hint 文案没有跟着改**

`git diff f07eba5~1 f07eba5 -- internal/upstream/hint.go` 为空。上游 `upstream/main:internal/upstream/hint.go:63-66` 仍是（f07eba5 前后一致）：

```go
	case ErrUpstreamBusy:
		// 6021 queue.full：渠道级容量满载。措辞明确「不是账号问题、换号无意义」，
		// 指引客户端短等待后重试（SDK 指数退避天然适配）。
		return "upstream admission queue is full (channel-level capacity, not an account problem); retry after a short wait"
```

本仓 `internal/upstream/hint.go:64-67` 是另一份文案：

```go
	case ErrUpstreamBusy:
		// 渠道级容量信号（6020 queue.waiting / 6021 queue.full）：换号打的是同一堵墙，
		// 客户端唯一有效的动作是等一小会儿再重试（网关已带 Retry-After）。
		return "upstream channel is at capacity (queue waiting/full); retry after a short wait — switching accounts will not help"
```

结论：**两处都不用改**（本仓文案已覆盖 `queue.waiting`，且「换号无用」对客户端仍然成立——轮转不是绕过排队，只是骑间歇窗口）。但注释第一句「客户端唯一有效的动作是等」在改成轮转后略显陈旧，见 §6「需决策」。

---

## 4. 两句判据：6020 / 6021 的识别条件，以及 6000 与它们的语义分界

### 4.1 识别条件（`upstream/client.go` 的 `Classify`，`hasBusinessCode`）

`hasBusinessCode`（`upstream/main:393-422`；本仓同名同实现，`internal/upstream/client.go:380` 起）：

```go
// hasBusinessCode reports whether a JSON error envelope contains an exact
// business code in a field named "code". Upstream envelopes vary between
// top-level and nested error/data objects, so walk the decoded structure.
func hasBusinessCode(body, want string) bool {
	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return false
	}
	var walk func(any) bool
	walk = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			if code, ok := node["code"]; ok && strings.TrimSpace(fmt.Sprint(code)) == want {
				return true
			}
			for _, child := range node {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range node {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	...
}
```

判定点（`upstream/main:669-678`，本仓 `client.go:620-627`，**逐字相同**）：

```go
	// 6020「queue.waiting.title」/ 6021「queue.full.title」渠道级排队/队列满：上游
	// 容量信号，不是账号限流。判在 429 兜底之前——按 ErrSoftRate 罚号会与全冷却兜底
	// 互相喂（兜底把冷却号再送上游，再撞又续订 600s），把几秒的排队放大成整段时间
	// 全量 429（6021：2026-10-15 实案 18 次全落 soft_rate，PR #180/#185；6020：
	// 2026-10-24 实案同样落 soft_rate 账号级冻结 600s）。两个 code 同族同判。
	// 任意状态码都认（防上游换状态码）；结构化 code 主判，requestId 等字段恰好
	// 含 "6020"/"6021" 不误伤（hasBusinessCode 只认 "code": 键）。
	if hasBusinessCode(body, "6020") || hasBusinessCode(body, "6021") {
		return ErrUpstreamBusy
	}
```

三个必须记住的性质：
1. **两个 code 同族同判**（一个 if 里 `||`），处置完全一致。
2. **任意状态码都认**（不绑 `429`），防上游换状态码；判在 `status == 429 → ErrSoftRate` 兜底**之前**。
3. **只认结构化 `"code"` 键**（且允许 `"6020"` 字符串形态，`fmt.Sprint` 归一）。`requestId` 里含 `6021` 不误命中——本仓 `client_test.go:118` 有这条断言。

### 4.2 6000（模型级限流）vs 6020/6021（渠道排队）的处置分界

上游注释原文（三处，按代码位置排）：

**(a) `client.go:246-250` —— 6000 是「模型级用量配额」，且必须文案佐证：**

```go
// modelUsageLimitCode 上游「用量频率限制」的业务码（2026-10 实测出现在 kimi 系模型）。
// 与 6004 同属模型级——msg 明确写「您也可以切换其他模型继续使用」——但 code 不同，
// 只收进 modelRateLimitCodes 会连账号级 6000 一起放宽，故单独判定：code 6000 且
// 文案命中 modelSwitchModelHint 才按模型级处理。
const modelUsageLimitCode = "6000"
```

**(b) `client.go:669-678`（见 4.1）—— 6020/6021 是「渠道容量信号，不是账号限流」：**
> 「上游容量信号，不是账号限流。判在 429 兜底之前——按 ErrSoftRate 罚号会与全冷却兜底互相喂…把几秒的排队放大成整段时间全量 429」

**(c) `handler.go:1828-1844`（终端映射处的正反面对照，决定「发不发 Retry-After」）：**

```go
	} else if ue == nil && modelBlockAllQuota(modelBlock) {
		// 全池该模型的锁都是「用量配额」（6004/6000：上游明说用量已超出频率限制、
		// 将在某时刻重置）。重置前重试**必然同样失败**，所以按配额语义回 429 + 上游
		// 原文，且**不发 Retry-After**：客户端普遍把它当「睡多久」的指令（codebuddy
		// CLI 直接 delayMs = retry-after×1000，不设上限），把几十分钟的冷却原样报
		// 出去就是客户端静默挂起——比报错更难排查。
		// 与下面 14003 的可重试分支互补：那个是瞬时繁忙（等就有效），这个是额度窗口
		// （等到重置前都无效）。
```

**分界表（移植时按这张表保持互不干扰）**：

| 维度 | 6000（+6004） | 6020 / 6021 |
|---|---|---|
| 分类归属 | `status==429` → **`ErrSoftRate`**，再由 `IsModelRateLimit(body)` 提为**模型级** | 独立 kind **`ErrUpstreamBusy`**（判在 429 兜底之前） |
| 信号性质 | 该 (账号, 模型) 的**用量额度窗口**用满 | 渠道**全局排队/队列满**（按 `client_user_type` 共享，与账号无关） |
| 换模型有用吗 | **有用**（换等价模型立即可用 → `ModelFallback` 只对 6004 触发） | 无用（同一堵墙） |
| 换号有用吗 | 有用（别的号可能没超量） | **无用**（队列共享），但可骑「间歇空窗」 |
| 该等吗 | **不该等**：重置前必然同样失败 | **该等**：几秒~几十秒就恢复 |
| 状态码 | 429 + 上游业务码（`quotaCode`） | 429 + `code="upstream_busy"` |
| `Retry-After` | **不发**（防止客户端静默挂起几十分钟） | **发**（body `data.retry_after`／兜底 5s／封顶 600） |
| 账号处置 | 模型级软冷却（`CooldownSoftForModel`） | **零动作**（不冷却/不熔断/不喂连败） |

一句话对照：**同为 429，一个「别等了」，一个「等一会儿」——差别全在发不发 `Retry-After`。**

---

## 5. 11102 负缓存 TTL：6h/24h → 2m/10m（`4f79fc8` 的 pool 部分）

### 5.1 常量与注释 diff（`internal/pool/cooldown.go`）

```diff
 // modelBlock TTL 常量（11102 负缓存退避）：
-// 首次命中 6h；半开到期后允许放行重试，再次命中 TTL = base × 2^min(hits-1, shift)；
-// 封顶 24h（最多一天再试一次）。该模型请求成功即由 BlockModelClear 清除。
+// 首次命中 2m；半开到期后允许放行重试，再次命中 TTL = base × 2^min(hits-1, shift)；
+// 封顶 10m（最多十分钟再试一次）。该模型请求成功即由 BlockModelClear 清除。
+//
+// 为什么从 6h/24h 缩到 2m/10m（2026-10-27，吸收 ithtelab/workbuddy-manager#312 的
+// 0033 与 issue #308 的复现）：11102 不总是「这号真没这模型」——它也会由路径级、
+// 上游瞬时抖动、分类误判触发。原参数把一次偶发误判放大成 (账号,模型) 锁死 6 小时，
+// 多号轮一圈就是整池该模型 503，而实测同一账号两分钟就能恢复。退避仍然保留：
+// 真没模型的账号反复命中会升到封顶 10 分钟，不会变成每请求重试。
 const (
-	modelBlockBaseTTL = 6 * time.Hour
+	modelBlockBaseTTL = 2 * time.Minute
 	modelBlockShift   = 4
-	modelBlockMaxTTL  = 24 * time.Hour
+	modelBlockMaxTTL  = 10 * time.Minute
 )
```

本仓现值（`internal/pool/cooldown.go:342-349`）仍是旧的 `6h / 4 / 24h`。

### 5.2 退避曲线（不变，只是基数与封顶缩短）

写入点：`internal/pool/cooldown.go:360-391`（本仓 `BlockModelBackoff`，行号与上游一致）：

```go
	hits++
	ttl := modelBlockBaseTTL
	if d := ttl * (1 << uint(min(hits-1, modelBlockShift))); d < modelBlockMaxTTL {
		ttl = d
	} else {
		ttl = modelBlockMaxTTL
	}
```

曲线：`TTL = base × 2^min(hits-1, 4)`，超过 `max` 就取 `max`。

| hits | 旧（base 6h / max 24h） | 新（base 2m / max 10m） |
|---|---|---|
| 1 | 6h | 2m |
| 2 | 12h | 4m |
| 3 | 24h（=max） | 8m |
| 4 | 24h（=max） | 10m（封顶） |
| 5+ | 24h | 10m |

- 指数上限 `modelBlockShift = 4`（`hits-1` 超过 4 就不再翻倍），所以 `base<<4 = 32m > max 10m` → **阶梯真的能走到封顶**（这正是新守卫用例断言的性质；旧的 `6h<<4 = 96h > 24h` 也成立）。
- `Hits` 不落盘（`entry.go:480/481` 注释：重启后 11102 退避从基数重新学习），所以重启会把 `Hits` 归 0，回到 base 档。

### 5.3 `4f79fc8` 里同步改的注释（3 处 + 用例注释）

```diff
--- a/internal/pool/entry.go
+++ b/internal/pool/entry.go
@@ -188,7 +188,7 @@ type RateLimitedModel struct {
 // modelCooldown 单个 (账号, 模型) 的模型级独立冷却记录。
 // 承载两种「该模型在此账号上不可用」语义：
 //   - 6004 模型级限流：Until 对齐上游重置墙钟；ResetAt 记录权威恢复时刻。
-//   - 11102 该后端无此模型：Until 为指数退避 TTL（6h 起、封顶 24h）；Hits 记录
+//   - 11102 该后端无此模型：Until 为指数退避 TTL（2m 起、封顶 10m）；Hits 记录
 //     累计命中次数驱动退避（6004 无 hits 概念，Hits 恒 0）。

@@ -590,7 +590,7 @@ type stateAccount struct {
-// Hits 不落盘（重启后 11102 退避从 6h 基数重新学习，同 modelCost 口径）。
+// Hits 不落盘（重启后 11102 退避从 2m 基数重新学习，同 modelCost 口径）。
```

本仓对应行：`internal/pool/entry.go:153`（`6h 起、封顶 24h`）与 `internal/pool/entry.go:480`（`从 6h 基数重新学习`）。**两句都要一起改**，否则注释与常量自相矛盾。

`modelblocked_test.go` 里原有的 `TestModelForbiddenReasonClearedAndTagged` 注释也同步改了（本仓没有这个测试，见 §7）。

### 5.4 新增的「常量守卫」用例（上游 `internal/pool/modelblocked_test.go:226-246`）

```go
// TestModelBlockTTLShortEnoughToSelfHeal 11102 负缓存必须短到能自愈。
//
// 原参数 6h 起、封顶 24h，把一次偶发误判（11102 不总是「这号真没这模型」）放大成
// (账号,模型) 锁死整天，多号轮一圈就是整池该模型 503（ithtelab/workbuddy-manager#312
// 的 0033 / issue #308：实测同一账号两分钟就恢复）。退避本身保留，只是基数与封顶缩短。
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
	// 退避仍需存在：base × 2^shift 必须真的超过封顶，否则 Hits 机制形同虚设。
	if modelBlockBaseTTL<<modelBlockShift < modelBlockMaxTTL {
		t.Fatalf("退避到不了封顶：base<<%d = %v < max %v", modelBlockShift, modelBlockBaseTTL<<modelBlockShift, modelBlockMaxTTL)
	}
}
```

三个上界（`>5m` / `>15m` / `max<base` / `base<<4<max`）用的是「区间断言」而非「等值断言」——所以将来微调（如 3m/12m）不会红，只有回到小时级才红。这是可以照抄的写法。

### 5.5 为什么不用「每请求重试」

上游 commit message 与代码注释给的理由是同一件事：

1. **11102 不总是真的「这号没这模型」**：路径级差异、上游瞬时抖动、分类误判都会吐同一个码。做成「每请求都重试」= 把一次偶发误判立刻转成「每个请求都白打一次上游」（`MaxRotate` 倍放大 + 日志风暴），并且**永远学不到任何东西**——池里会持续把该号该模型选中再被拒。
2. **负缓存的价值是「学习」而不是「惩罚」**：命中一次就记住「这个 (账号,模型) 组合暂时别试」，把请求挤到别的号/别的模型上。真正没模型的号会不断命中 → 升到封顶 10 分钟，仍然是「十几分钟试一次」的极低探测频率，成本可忽略。
3. **6h/24h 的害处是「学习过度」**：多号池里每个号各锁一次 = 整池该模型 503，而实测同一账号 2 分钟就恢复。所以收敛的是**基数与封顶**，不是取消退避。
4. 配套自愈通道没变：`BlockModelClear`（`cooldown.go:397`）在该模型请求成功后立即清除条目，所以「权限/模型恢复」不必等退避到期。

---

## 6. 本仓落地差异 + 移植清单

### 6.1 差异逐条（本仓 `main` @ `b947f99` vs 上游 `80e844f`）

| # | 位置 | 上游（80e844f） | 本仓现状 | 影响 |
|---|---|---|---|---|
| D1 | `internal/upstream/client.go` | 有 `parseQueueRetryAfter`（:498-520） | **没有** | 6020 的 body 口径拿不到 → 只能 5s 兜底 |
| D2 | `internal/upstream/client.go:1294-1298` | 头解析失败且有 `else if kind == ErrUpstreamBusy` 找 body | 只有头解析 | 同上 |
| D3 | `internal/server/handler.go:1459-1480` | 轮转 + `continue`（:1439-1447） | **fail-fast 503 + return** | 队列抖动直接打死任务（P0 故障形态） |
| D4 | `internal/server/handler.go:1708-1728`（末端 `switch ue.Kind`） | 有 `case ErrUpstreamBusy`（429，:1757-1777） | **没有这个 case**（只有 `ErrSoftRate`/`ErrWafBlock`/`ErrModelBlocked`） | 即便改成轮转，末端也会落 503 `no_healthy_account`（只靠上游原文覆盖 msg） |
| D5 | `internal/server/handler.go:1747` | `retryAfterSec` 声明在 `errors.As` **之前**（:1748） | 声明在 `switch` **之后**（:1747） | 不提前 → 新 case 无法写该变量（编译错） |
| D6 | `internal/server/handler.go` imports | 有 `"math"`（用 `math.Ceil`） | **无 `math`** | 需要加 import，或用本仓既有 `int()+1` 风格 |
| D7 | `internal/upstream/client.go:46` | 行尾注释已（部分）陈旧，仍写「不轮转，末端 503」 | 同样写「不罚号、不轮转，末端 503 透传原文」 | 两处都需要更新为新语义 |
| D8 | `internal/upstream/hint.go:64-67` | 「admission queue is full (channel-level capacity, not an account problem)」 | 「upstream channel is at capacity (queue waiting/full); retry after a short wait — switching accounts will not help」 | 文案不同但不冲突；本地版已覆盖 `queue.waiting` |
| D9 | `internal/pool/cooldown.go:342-349` | `2m / 4 / 10m` | `6h / 4 / 24h` | 11102 误判锁死 6h |
| D10 | `internal/pool/entry.go:153`、`:480` | 「2m 起、封顶 10m」「从 2m 基数」 | 「6h 起、封顶 24h」「从 6h 基数」 | 注释与常量需同步 |
| D11 | 测试文件组织 | `handler_test.go` 里的 `TestChatQueueFullFailsFastWithoutPenalty`（改名 `TestChatQueueBusyRotatesThen429WithoutPenalty`）+ 新增 `TestChatQueueWaitingRetryAfterFromBody` | `handler_apilayer_test.go:14` 的 `TestChatQueueFullFailsFastWithRetryAfter`（断言 503，且**不查调用次数**） | 移植后必然红，见 §7 |
| D12 | `internal/pool/modelblocked_test.go` | 有 `TestModelForbiddenReasonClearedAndTagged` + 新增 `TestModelBlockTTLShortEnoughToSelfHeal` | 本仓没有前者（11158 的清理测试在 `modelbusy_test.go:104 TestBlockModelClearModelForbidden`），也没有 TTL 守卫 | 需要新增 TTL 守卫用例 |
| D13 | `internal/server/handler.go` 轮转循环 | 无「首帧否决」/ realm 分离 / 模型白名单 | **有**（首帧否决 `:1502-1524`、realm 选号 `:1166`、`modelAllowed`） | 本仓改动必须落在本仓结构里，不能整段 replace |
| D14 | `.trellis/spec/backend/error-handling.md` | —（上游无此文件） | `:39`、`:80`、`:94`、`:142` 四处写着「fail-fast、不轮转、503 `upstream_busy`」 | 移植后 spec 必须同步（由主代理走 update-spec） |

### 6.2 本仓移植清单

#### A. 必须改（不改就是 P0 未修）

| 目标文件 | 函数 / 位置 | 改法 |
|---|---|---|
| `internal/upstream/client.go` | 新函数 `parseQueueRetryAfter`，插在 `ParseRetryAfter` 之后（本仓 :477 后） | 原样照抄 §1 的函数体（`retryAfterSanity` 本仓已有，直接复用） |
| `internal/upstream/client.go` | `ChatStreamContext`（本仓 :1294-1298） | 头解析后加 `} else if kind == ErrUpstreamBusy {` 分支，调 `parseQueueRetryAfter(raw)` 写 `ue.RetryAfter` |
| `internal/server/handler.go` | `chatCompletions` 轮转循环 `ErrUpstreamBusy` 分支（本仓 :1459-1480） | 整段替换为：`applyErrorPolicy` → `fail` → `lastErr = &upstream.Error{Kind, Status: status, Msg: string(respBody), RetryAfter: uerr.RetryAfter}` → `if !rotateBackoff(i, r.Context()) { break }` → `continue`；注释按 §2.1 的三段「为什么」重写（保留 2026-10-15/2026-10-28 两个实案的因果） |
| `internal/server/handler.go` | `chatCompletions` 末端 `switch ue.Kind`（本仓 :1708-1728） | ① 新增 `case upstream.ErrUpstreamBusy`：`status = http.StatusTooManyRequests`、`code = "upstream_busy"`、`msg = "upstream queue is full; retry after a short wait"`、填 `retryAfterSec`（含 600 封顶与 5s 兜底）；② 把 `retryAfterSec := 0` 从 :1747 **上移**到 `var ue *upstream.Error`（:1705）之前 |
| `internal/server/handler.go` | imports（:4-30） | 加 `"math"`（用 `math.Ceil`）；或者改用本仓既有的 `int(d.Seconds())+1`（会多报 1s，不推荐） |
| `internal/pool/cooldown.go` | 常量块（:342-349） | `modelBlockBaseTTL = 2 * time.Minute`、`modelBlockMaxTTL = 10 * time.Minute`；注释按 §5.1 重写（含「为什么缩短」整段） |
| `internal/pool/entry.go` | `modelCooldown` 注释（:153）、`stateModelCooldown` 注释（:480） | `6h/24h` → `2m/10m` 两处文字 |
| `internal/pool/modelblocked_test.go` | 文件尾部 | 新增 `TestModelBlockTTLShortEnoughToSelfHeal`（原样照抄 §5.4，含四条区间断言） |
| `internal/upstream/client_test.go` | `TestParseSoftRateReset` 之后（本仓 :207 之后） | 新增 `TestParseQueueRetryAfter`（照抄 §7.3，含封顶断言） |
| `internal/server/handler_apilayer_test.go` | `TestChatQueueFullFailsFastWithRetryAfter`（:12-45） | 改写为「轮转两号各一次 + 429 + Retry-After 5 + 零惩罚」，见 §7.1/§7.2 |

#### B. 需决策（生效前请主代理定口径）

| # | 决策点 | 选项 | 建议 |
|---|---|---|---|
| B1 | `ErrKind` 行尾注释（本仓 `client.go:46`） | (a) 只把「不轮转，末端 503」改成「请求内轮转，末端 429+Retry-After」；(b) 连上游 `client.go:50` 的陈旧写法也一并纠正 | 选 (b)。上游自己漏改了这句，抄过来会把错误语义写进文档 |
| B2 | `hint.go:64-67` 文案是否跟改 | (a) 不动；(b) 去掉「— switching accounts will not help」，改成「gateway already rotated; the queue is shared per user type」 | 倾向 (b)：改成轮转后「换号无用」这句会让运维误读为「网关没在换号」。但 `hint_kinds_test.go:21` 断言子串 `"will not help"`，改文案必须同步改断言（见 §7.4） |
| B3 | 排队失败是否解绑会话粘性 | 上游沿用 `fail()` → 会 `unbindSticky()`；但排队与账号健康无关，解绑会让下一轮重新随机分配 | 保持上游行为（`fail()`）以降低分叉面。若要「不因排队解绑」，需在本分支改为 `releaseHeld()` 而非 `fail()` —— **不建议**在第一版就偏离上游 |
| B4 | 存量 `state.json` 里升级前写入的 11102 条目（`Until` 可能是 now+6h） | (a) 不动，等自然到期（最长 6h）；(b) 恢复时把 `reason` 以 `11102`/`11158` 开头的条目 `Until` 钳到 `now+modelBlockMaxTTL` | 倾向 (b)：`persist.go:226-234` 恢复路径加一次钳制，否则「上线即时收窄」这个卖点要等 6h 才成立。代价是 `modelcooldown_test.go:587 TestModelCooldownsPersist` 需确认只断言条数（当前只断言 `len`，安全） |
| B5 | 首帧否决路径（本仓 `:1502-1524`）遇到 `ErrUpstreamBusy` 帧 | 该路径 `lastErr` 里**没有 `RetryAfter`**（`uerr` 为 nil），末端会落到 5s 兜底 | 接受现状（帧里本就没有 `data.retry_after` 结构，`FrameKind` 只从 `error.message` 走 `Classify`），在注释里写明这个回落 |

#### C. 不要照搬（照抄会破坏本仓结构或引入错误）

| # | 上游写法 | 为什么不能照抄 |
|---|---|---|
| C1 | 整段 `git diff` 直接 apply 到 `handler.go` | 本仓 `handler.go` 与上游差异很大（realm 分离选号 `:1166`、模型白名单 `modelAllowed`、首帧否决 `:1502-1524`、无 `ErrHardCredit`/`quota` 变量、`vetoedStreamFrame` 覆盖、`maxModelBusyWait` 替代上游 `busyWaitCap`）。hunk 行号与上下文都对不上，只能按「目标函数 + 语义」重写 |
| C2 | 上游 `retryAfterSec` 旁的 `quotaCode` / `upstreamMsgPassed` 上下文 | 本仓没有 `quota` 布尔、没有 `hardCreditMessage`、没有 `AllHardCreditForRealm` 分支。新 case 只写 `status/code/msg/retryAfterSec` 四件事 |
| C3 | 上游 `handler_test.go` 的 `TestChatQueueBusyRotatesThen429WithoutPenalty` 原样贴 | 本仓测试基础设施不同：`newFakeUpstream` 在 `handler_test.go:39`、`testPoolWith`（同名）、但断言用的账号 UID 与 credits 构造不同（本仓用 `bad`/`good` + `SetCredits`）。要按本仓 `handler_apilayer_test.go` 既有骨架改写 |
| C4 | 上游 `client.go:50` 的行尾注释（「不轮转，末端 503」） | 上游忘了改，是**陈旧注释**，不是设计意图 |
| C5 | 上游 `TestMain` 里 `busyWaitCap = 0` | 本仓没有 `busyWaitCap`，等价物是 `const maxModelBusyWait = 5s`（`handler.go:1878`）。本仓 `TestMain`（`handler_test.go`）只关 `chatLogEnabled`；轮转退避会真睡 500ms/次，新测试要注意耗时（2 号池只有 1 次退避 ≈ 500ms，可接受） |
| C6 | 上游 `internal/pool/modelblocked_test.go` 的 `TestModelForbiddenReasonClearedAndTagged` | 本仓该测试已拆到 `modelbusy_test.go:104 TestBlockModelClearModelForbidden`（语义等价，断言更窄）。只补 TTL 守卫，不要重复搬 |

### 6.3 落地后的形状（本仓视角的伪 diff，供实现者对照）

```go
// internal/server/handler.go —— 轮转循环内
if kind == upstream.ErrUpstreamBusy {
    h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
    fail(acct.UID)
    lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody), RetryAfter: uerr.RetryAfter}
    if !rotateBackoff(i, r.Context()) {
        break
    }
    continue
}
```

```go
// internal/server/handler.go —— 末端（注意 retryAfterSec 已提前声明）
case upstream.ErrUpstreamBusy:
    status = http.StatusTooManyRequests
    code = "upstream_busy"
    msg = "upstream queue is full; retry after a short wait"
    if ue.RetryAfter > 0 {
        if secs := int(math.Ceil(ue.RetryAfter.Seconds())); secs > 0 {
            retryAfterSec = secs
            if retryAfterSec > maxRetryAfterSec {
                retryAfterSec = maxRetryAfterSec
            }
        }
    }
    if retryAfterSec == 0 {
        retryAfterSec = 5
    }
```

后端写头逻辑**无需新改**：本仓 `handler.go:1799-1801` 已经在 `retryAfterSec > 0` 时写 `Retry-After`，`writeOpenAIErrorHint`（:1810）会把 `status`/`code`/`msg`/`hint` 一起落盘。

---

## 7. 既有回归：上游改了什么断言，本仓哪些会红

### 7.1 上游测试断言改动（`internal/server/handler_test.go`）

`TestChatQueueFullFailsFastWithoutPenalty` **改名 + 改断言** → `TestChatQueueBusyRotatesThen429WithoutPenalty`：

```diff
-	if rec.Code != 503 {
-		t.Fatalf("code=%d body=%s (want 503 fail-fast，非 429)", rec.Code, rec.Body)
+	if rec.Code != http.StatusTooManyRequests {
+		t.Fatalf("code=%d body=%s (want 429，轮转耗尽后的可重试语义，非 503)", rec.Code, rec.Body)
 	}
 	if !strings.Contains(rec.Body.String(), "6021") || !strings.Contains(rec.Body.String(), "b1bc87ea") {
-		t.Errorf("503 body 应透传上游原文（含 code+requestId）: %s", rec.Body)
+		t.Errorf("429 body 应透传上游原文（含 code+requestId）: %s", rec.Body)
 	}
 	if ra := rec.Header().Get("Retry-After"); ra != "5" {
 		t.Errorf("Retry-After=%q want 5（上游未给重置口径时的本地兜底）", ra)
 	}
-	if calls["Bearer at-busy1"] != 1 || calls["Bearer at-busy2"] != 0 {
-		t.Errorf("calls=%v want 只打一次上游、busy2 号 0 次（不轮转）", calls)
+	if calls["Bearer at-busy1"] != 1 || calls["Bearer at-busy2"] != 1 {
+		t.Errorf("calls=%v want 两号各一次（轮转、tried 不重复选号）", calls)
 	}
 	// 账号完好：无冷却、无禁用、无熔断计数、无 errTotal（ErrSoftRate 会冷却+喂 streak）。
 	st, _ := p.Status("busy1")
```

`Retry-After == "5"` 这条**没变**（6021 body 无 `data.retry_after` → 兜底 5）。变的只有：状态码 503→429、`busy2` 调用次数 0→1。

### 7.2 上游新增端到端用例（`handler_test.go`，紧随其后）

```go
func TestChatQueueWaitingRetryAfterFromBody(t *testing.T) {
	const upstreamBody = `{"code":6020,"msg":"queue.waiting.title","requestId":"01a181ca3b67934d01c3f64599c8490c","data":{"queue_position":2502,"queue_size":"999+","retry_after":40,"estimated_wait":12510}}`
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 429, upstreamBody, false
	})
	p := testPoolWith(&auth.Auth{UID: "q1", AccessToken: "at-q1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429 (body=%s)", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "40" {
		t.Errorf("Retry-After=%q want 40（data.retry_after 透传，非本地兜底）", ra)
	}
}
```

### 7.3 上游新增单测（`internal/upstream/client_test.go:229-264`，紧跟 `TestParseSoftRateReset`）

```go
func TestParseQueueRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
		ok   bool
	}{
		{"6020 真机形态", `{"code":6020,"msg":"queue.waiting.title","requestId":"01a181ca3b67934d01c3f64599c8490c","data":{"queue_position":2502,"queue_size":"999+","retry_after":40,"estimated_wait":12510}}`, 40 * time.Second, true},
		{"retry_after=0（不该出现的边界）", `{"code":6020,"data":{"retry_after":0}}`, 0, false},
		{"6021 无 data（queue.full 无等待口径）", `{"code":6021,"msg":"queue.full.title","requestId":"b1bc87ea"}`, 0, false},
		{"非 JSON", `upstream unavailable`, 0, false},
		{"空 body", ``, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseQueueRetryAfter([]byte(c.body))
			if ok != c.ok {
				t.Fatalf("ok=%v want %v (body=%s)", ok, c.ok, c.body)
			}
			if ok && got != c.want {
				t.Errorf("got=%v want %v", got, c.want)
			}
		})
	}
	// 上限封顶：异常大的 retry_after 按 retryAfterSanity 截断，不把天文数字带回
	//（estimated_wait 是另一个字段不会误入；防上游口径漂移）。
	if d, ok := parseQueueRetryAfter([]byte(`{"code":6020,"data":{"retry_after":999999}}`)); !ok || d != retryAfterSanity {
		t.Errorf("cap: got=%v,%v want %v,true", d, ok, retryAfterSanity)
	}
}
```

### 7.4 本仓现有相关测试 + 移植后预期

| 文件 | 测试 / 断言 | 移植后 |
|---|---|---|
| `internal/server/handler_apilayer_test.go:14` `TestChatQueueFullFailsFastWithRetryAfter` | `rec.Code != http.StatusServiceUnavailable` → fail（**会红**）；`Retry-After` 非空（仍过）；body 含 `"6021"`（仍过）；`bad`/`good` 都不 cooling/disabled/breaker（仍过） | **红 1 处**：`want 503` → 改成 `http.StatusTooManyRequests`。另外该测试**没有断言调用次数**，应补 `calls` 计数并断言两号各 1 次（否则「是否轮转」这条回归没被钉住）。改造骨架：给 fake 加 `calls[authz]++`（同文件 `:49-52` 的 `TestChatToolMismatchFailsFast` 已有此写法可直接借） |
| `internal/server/modellock_test.go:88` `TestMaxRetryAfterCap` | `maxRetryAfterSec != 600`；`45m` 截到 600 | 不受影响（常量不变）。可选：把 6020 的「body 7200s → 末端 600」这条链也加进本测试 |
| `internal/upstream/client_test.go:113-118` | `Classify` 表：`429 + code 6020/6021 → ErrUpstreamBusy`；`{"code":9999,"msg":"x","requestId":"6021"} → ErrSoftRate` | 不受影响（分类逻辑未变）。**建议**在 `:136` 之后补 `TestParseQueueRetryAfter`（§7.3） |
| `internal/upstream/hint_kinds_test.go:17-22` | `GatewayHint(ErrUpstreamBusy, ...)` 必含 `"will not help"` | 只有改了 `hint.go` 文案（决策 B2 选 b）才会红 |
| `internal/pool/modelbusy_test.go:61` `TestCooldownModelBusyDoublesAndCaps` | 14003 的 `10s→20s→40s→modelBusyMax` 阶梯 + `Hits` 累计 | 不受影响（`modelBusy*` 常量与 11102 常量互相独立） |
| `internal/pool/modelblocked_test.go` | 只断言 `Blocked`/`Count`/`Reason`/`Until` 取最早值，**不查 TTL 具体时长** | 不受 TTL 收窄影响（安全）。需**新增** TTL 守卫用例 |
| `internal/pool/modelcooldown_test.go:337` `TestRateLimitedModelKindModelUnavailable` | `BlockModelBackoff` → `Kind == "model_unavailable"` | 不受影响 |
| `internal/pool/modelcooldown_test.go:587` `TestModelCooldownsPersist` | 重载后 `len(modelCooldowns) == 1` | 若采纳决策 B4（恢复时钳 `Until`），确认只断言条数即可（当前是，安全） |
| `internal/server/handler_test.go` 全部 503 断言（`:1194/:1213/:1243/:1284`） | 都是 `healthz` 的 503，与 chat 无关 | 不受影响 |
| `.trellis/spec/backend/error-handling.md:39/:80/:94/:142` | 文档：「6020/6021 → 立即返回、不轮转、503 `upstream_busy` + Retry-After」 | **必须同步改**（否则 spec 与实现相反，后续 agent 会照着旧 spec 回退） |

### 7.5 建议的验证命令

```powershell
go build ./...
go vet ./...
go test ./internal/upstream/ -run 'TestParseQueueRetryAfter|TestClassify' -count=1
go test ./internal/server/ -run 'TestChatQueue|TestMaxRetryAfterCap' -count=1
go test ./internal/pool/ -run 'TestModelBlock|TestCooldowns' -count=1
go test ./... -count=1
```

---

## Caveats / 未决

1. **本仓没有 `busyWaitCap`**：上游 `TestMain` 把它置 0 加速测试；本仓是 `const maxModelBusyWait = 5s`（`handler.go:1878`），不可注入。新加的轮转测试若恰好命中「14003 模型忙」等待分支会真睡 5s——但本次新增的两个用例都不走该分支，安全。
2. **`f07eba5` 的 `go.mod` 模块路径** 是 `github.com/misakano7545/...`，本仓是 `github.com/linguo2625469/...`；任何粘贴的 import 都要替换。
3. **上游 `client.go:50` 是陈旧注释**（仍写「不轮转，末端 503」），`f07eba5` 没同步。这是上游的疏漏，不是设计意图——已列进「不要照搬 C4」。
4. **本仓终端 switch 缺两个 case**（`ErrUpstreamBusy` 与 `ErrHardCredit`）。`ErrHardCredit` 的缺失属于既有分叉（本仓走 `modelBlockAllQuota` / `quotaCode` 机制），**不属于本次任务**，不要顺手补。
5. **未运行任何测试**：本文件全部结论来自 `git show`/`git diff`/源码阅读，没有执行 `go test`（research 阶段不改代码、不跑构建）。
6. **`4f79fc8` 的另两处（身份句式 `sanitizeIdentitySentenceRe`、DSH `@` 段 workspace root→project root）不在本任务范围**，未展开取证。
