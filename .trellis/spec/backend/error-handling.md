---
name: backend-error-handling
description: 错误分类、换号/罚号/fail-fast 三分法、上游错误码映射、提示词与验证语义
paths:
  - internal/server/**
  - internal/upstream/client.go
  - internal/upstream/hint.go
  - internal/upstream/verify.go
  - internal/pool/**
  - internal/httpauth/**
---

# 错误处理

本仓库的错误处理不是"打日志返回 500"，而是**先分类，再决定代价**。每一步都要回答两个问题：这一跳该不该换号？这个号该不该被罚？

## 类型化错误

```go
// internal/upstream/client.go
type ErrKind int          // 每个常量注释写明该类的处置策略
type Error struct { Kind ErrKind; Status int; Msg string; RetryAfter time.Duration }
```

- `Classify(status, body)` 是**唯一**的判定入口，在 `ChatStreamContext` 里对每个上游响应调用一次。
- 传输层/读 body 失败返回**普通 error，不是 `*Error`**：半截 body 没有可信语义，不进入分类、不参与罚号。
- 其它包不要自己判断上游错误码文本。业务码匹配走 `hint.go` 的 `codeMarker` / `HasCodeMarker`（容忍 JSON 空白与引号），**禁止整段文本子串匹配**。

## 分类顺序（顺序本身是语义）

`Classify` 的判定顺序固定，改顺序等于改行为：

```
IsModelBlocked(11102/11158, 仅 400/404)
→ 402（余额耗尽 → 硬冷却）
→ sessionDeadMarkers（登录态失效）
→ accountFaultMarkers         ← 必须早于 429
→ 429 + code 14018 → 硬冷却
→ 6020 / 6021 → 上游忙
→ status == 429 → 软限流
→ hardMarkers（余额类）
→ softRateMarkers
→ 11115
→ 11148
→ 404
→ >= 500
→ WAF 403
→ 网关 401
→ 11135 / invalidImageMarkers
→ 11133（模型参数非法）
→ contentBlockedMarkers
→ 11101 / Unmarshal chat params failed
→ ErrClient（请求级兜底）
```

`14003`（模型忙）必须在 `IsModelRateLimit` 之前判，否则会套上 `soft_rate` 的 600s→2h 阶梯，把一次抖动放大成整池十分钟不可用；6004/6000 的配额判断同样要先排除 14003。

另外 **6004/6000 的处置有前置条件**：只有 body 里解析出「将在 … 重置」的时间时才是"只写模型级、不写 `until`"；没有重置文案时走的是**账号级** `CooldownSoftRate`，模型条目只落一条审计（不产生模型豁免）。因此"6004 后切个模型就能继续用"只在带重置文案的形态下成立。

## 处置矩阵

处置由 `handler.applyErrorPolicy`（罚号）与轮换循环（换号）两处共同决定，下表按"上游信号 → 这两处各做什么"记录。**换号与罚号是两件独立的事**：有换号但不罚的分支（`ErrClient` 只喂连败），也有罚号后立刻终止轮转的分支（WAF IP 闸命中）。

| 上游信号 | Kind | 换号 | 罚号 | 返回客户端 |
|---|---|:---:|:---:|---|
| 429（账号级，非 6004） | `ErrSoftRate` | 是 | 账号级软冷却（指数退避，封顶 `soft_rate_max`） | 429 `rate_limit_exceeded` |
| 429 + 6004/6000，**body 带「将在 … 重置」文案** | `ErrSoftRate` + `IsModelRateLimit` | 是 | **仅模型级** `CooldownSoftForModel`：写 `modelCooldowns[model]`、按上游墙钟，**不写 `until`**；该账号切模型立即可用 | 429，`code` 为数字 6004/6000，**无 `Retry-After`** |
| 429 + 6004/6000，**无重置文案** | 同上 | 是 | **账号级** `CooldownSoftRate`（有 `Retry-After` 头则冷却到该时刻，否则 `soft_rate` 有界退避）+ `RecordModelRateLimitAudit`：模型条目只作审计展示，**不产生模型豁免** | 同上 |
| 14003 模型忙 | `ErrSoftRate` + `IsModelBusy` | 是 | 模型级"忙"冷却（10s 起，×2，封顶 1m），不套 `soft_rate` 阶梯 | 503 `model_busy` + `Retry-After` |
| 402 / 10001 / 14001 / 余额标记 | `ErrHardCredit` | 是 | 硬冷却到次日 04:00 | 最终 503 |
| 401 + 12153（**聊天路径**） | `ErrSessionDead` | 是 | **立即 `Disable`**（`"12153 session dead"`） | 按剩余可用决定 |
| 401 + 12153（**保活任务**） | `ErrSessionDead` | — | `NoteSessionDead` 连续 3 次才禁用，刷新成功则清零（P0-1：13 个 disabled 号全是立即杀号的误判） | — |
| 11102 / 11158 | `ErrModelBlocked` | 是 | 模型级封禁（6h 起，×2，封顶 24h） | 400 `model_unavailable` |
| 6020 / 6021 渠道排队/队列满 | `ErrUpstreamBusy` | **否：立即返回** | **否**（`applyErrorPolicy` 该分支零动作） | 503 `upstream_busy` + `Retry-After`（上游未给时 5s） |
| 11115 / 11135 / 11101 / 11133 / 11148 | 请求级 | **否** | **否** | 400，透传上游原文 |
| 内容策略拦截 | `ErrContentBlocked` | 见下节 | **否** | 400 `content_blocked` |
| WAF 403 | `ErrWafBlock` | 未激活 IP 闸→继续换号；**激活则终止轮转** | **是**：账号级软冷却（基数 `wafCooldownBase` 抖动后 ≈45–75s，`softStreak` 指数升级，封顶 `soft_rate_max`）；`Retry-After` 头优先；**不 `Disable`** | IP 闸激活时 503 `waf_ip_blocked` |
| 上游超时 / 流停滞 | —（传输层错误，非 `*Error`） | **否** | **否** | 503 |
| 其它传输错误（连不上 / 网络抖动） | —（同上） | **是**（抖动退避后 `continue`） | `NoteFailures`：连败达 `degrade_threshold` 才临时出池，单次不罚 | 503（轮转耗尽后） |
| 404 | `ErrNotFound` | 是 | 固定 60s 软冷却（**不**走 `soft_rate` 指数） | — |
| 5xx | `ErrServer` | 是 | `NoteError` 熔断计数（`pool.breaker_*`） | — |
| 未分类 4xx（兜底） | `ErrClient` | 是 | `NoteFailures`（同一形态连续 N 次才降权） | — |
| 语义不明兜底（`ErrNone`） | `default` | 是 | **否** | — |

## 四条不动作/边界铁律

1. **请求级错误不换号**：参数非法、图片无效、提示词过长、内容拦截都属于"这个请求本身的问题"，再换十个号也一样。换号只会放大上游风控信号。
2. **上游容量信号 fail-fast**：6020/6021 是渠道排队/队列满，与账号健康无关。换号会把同一个无效请求放大 `MaxRotate` 倍，还会与全冷却兜底互相喂成整池停摆（历史事故）。因此立即 503 + `Retry-After`，交客户端按 SDK 退避重试。
3. **基础设施故障要分级**：**超时/停滞**意味着"换到别的号撞的是同一个慢上游"，所以不换号、不罚号；**普通传输错误**（连不上、连接被重置）则是单号视角的失败，换号但只喂连败计数，达阈才降权。
4. **冷却中不叠加**：已在冷却期内的重复失败不推进 streak、不延长 `until`。历史上"越重试越冷、整池被推到 2h 封顶"就是这条没守住。

## 提示词与内容拦截的特殊路径

- 模式为 `passthrough`/`append` 且尚未降级时，遇到 `ErrContentBlocked`：`degrade.Trigger()` → 用 `prompt.Degraded` 重写一次 → `delete(tried, uid)`（单号池也要拿到重试名额）→ `continue`。**只重试一次**。
- 第二次仍被拦：原样透传上游 body，不换号、不罚号。
- `ErrPromptTooLong` 一类必须透传上游原文，禁止用固定文案覆盖。

## 池内错误与"重试已经没用"的判定

- `handler.fail(uid)` 只对**本次粘性绑定的那个号**解绑，不会误解绑别轮的绑定。
- 粘性账号返回 nil（模型冷却 / 禁用 / 积分地板 / 在途已满）→ 解绑后走普通轮换；这里依赖 `pool` 的 `PickByUIDForModel` 返回 nil 的语义，别在 handler 里重复判一遍原因。

## 登录态验证

`internal/upstream/verify.go`：

```go
type VerifyVerdict int   // Alive / Dead / Inconclusive
func VerifySessionByRefresh(...) VerifyVerdict
```

- 刷新成功 = Alive；应用层 4xx `*Error` = Dead；5xx / 429 / WAF / 网关 401 = **Inconclusive**（既不算活也不算死）。
- 网关侧 401 必须先验证再处置：刷新后仍 401 才按登录态失效处理。
- 验证为 Dead 之后，**聊天路径立即 `Disable`**（这一次请求已经实证不可用，继续留在池里只会再失败一次）；**保活路径**才走 `NoteSessionDead` 的三次连续计数。两条路径的阈值不同是刻意的，不要把其中之一当成"统一状态机"。
- "Dead 但可用"（`SessionDeadButUsable`）只用于刷新前的路径。

## Go 侧错误风格

- 返回值错误，不用 panic（全仓库 `panic` 只出现在测试与真正的不可恢复初始化）。
- 包装用 `%w`，判定用 `errors.Is`（`os.IsNotExist` 看不穿包装）。
- **降级优于失败**：脏密钥条目跳过并 WARN、单个凭证文件解析失败跳过、Redis 不可用退化为 Noop、可选依赖缺失返回 501 —— 都不得阻断启动或已在服务的请求。
- 持久化失败不冒泡，只按节奏记日志（`pool` 的 `notePersistFail`：首次 + 每 12 次）。
- 日志只记一次：错误在产生处记录或在上报处记录，不要层层打。

## 反模式

- 用 `err.Error()` 字符串包含判断错误类型。
- 新增错误类别却不更新 [http-api-layer.md](./http-api-layer.md) 的状态码映射表与面板提示。
- 把 `Inconclusive` 当成 Dead 处理（会误禁号）。
- 把保活任务的"连续 3 次才禁用"套到聊天路径（会让一次实证失效的号继续被选中）。
- 把 6020/6021 当账号故障去换号（放大无效请求，且会喂成整池停摆）。
- 把"6004 只写模型级冷却"当成无条件规则（无重置文案时实际是账号级冷却）。
- 给 6004 配额响应加 `Retry-After`（客户端会整段睡死）。
- 在 `internal/upstream` 里调用 `pool` 的方法（依赖方向不允许，且会把惩罚决策埋进协议层）。
