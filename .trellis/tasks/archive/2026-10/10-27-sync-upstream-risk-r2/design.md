# 设计：同步上游第二轮风控修复

## 边界

| 层 | 文件 | 本次职责 |
|---|---|---|
| upstream | `refresh_singleflight.go` | 单飞结果发布契约、panic 释放、结果应用到等待者 |
| upstream | `verify.go` | `SessionDeadButUsable` 成为误禁判据的**唯一**来源（不改实现，改调用方） |
| auth | `auth.go` | `ExpiresAtValue` 加锁读取；`expiresAt` 毫秒归一；保存有界重试 |
| pool | `cooldown.go` | 模型级冷却的作用域与时长语义；按 realm 的阻塞判定 |
| pool | `persist.go` | dirty 语义、fsync、备份与损坏恢复 |
| pool | `state.go` / `entry.go` | 复用既有 `NoteSessionDead` / `ClearSessionDead`，不新增平行状态 |
| server | `handler.go` | 预刷新容忍、网关 401 阈值、6004/14003 分支、按域阻塞、短等待 |
| server | `budget.go` | 既有 setter 接线（实现已存在） |
| scheduler | `scheduler.go` | 保活不计死 |
| cmd | `main.go` | 预算字段从"需重启"清单移除并热改 |

## 关键契约

### C1 刷新单飞的发布契约

`refreshCall.done` 是等待者**唯一**的同步点。因此：

- leader 必须在 `close(done)` **之前**写 `call.res`；
- 等待者 `<-done` 之后只读 `call.res`，不得再依赖任何其他标志；
- leader 在刷新路径 panic 时，`res` 预置为明确错误（`refresh aborted: panic ...`），
  先发布并 `close`，再把 panic 原样抛出。

由此得到一条推论，写进代码注释：**任何"先 close 再赋值"的改动都会让失败变成功**。

### C2 结果应用到等待者的守卫

`refreshResult.applyTo(a, staleRT)`：

- 只在 `res.err == nil` 且 `a.RefreshToken == staleRT` 时写入；
- 响应省略的字段（`refreshToken`/`domain`/`expiresAt` 为零值）沿用旧值；
- 读写在 `a` 自身的锁内完成（与 `RefreshToken` 的写回段同锁，不引入第二把锁）。

判据说明：`staleRT` 是调用方发起刷新时持有的快照，key 也由它构成。RT 已变说明期间
已被别人刷新，覆盖只会把更新鲜的凭据写旧。

### C3 误禁判据唯一来源

`upstream.SessionDeadButUsable(a)` = `!a.NeedsRefresh(0)`，即 **AT 未过期**。
适用范围与 `verify.go` 注释一致：

- **适用**：保活、余额探测、请求前预刷新（这些路径上 AT 尚未被上游拒绝）；
- **不适用**：chat 拿到网关 401 的路径（AT 已被拒过），那里 refresh 判死应计死。

因此 R1-4 只改保活与预刷新；chat 的 401 路径走 R1-5 的连续阈值，而不是"看 AT"。

### C4 连续 session dead 的唯一计数器

已存在 `Pool.NoteSessionDead` / `ClearSessionDead` / `sessionDeadThreshold = 3`。
本轮不新增计数器，只把它接到此前绕过它的两条路径上（预刷新之外）：

- 保活：AT 不可用时计数；
- 网关 401 + refresh 判死：计数。

成功证据（refresh 成功、chat 成功、人工复活）仍由既有入口清零，不改。

### C5 冷却作用域矩阵

| 错误 | 作用域 | 时长 | 备注 |
|---|---|---|---|
| 6004 + 可解析重置时间 | `(账号, 模型)` | 上游墙钟，**不被 `soft_rate_max` 截短** | 脏值保护：过去时间→1ms；超过 7 天→按 7 天钳制 |
| 6004 无重置时间 | `(账号, 模型)` | `soft_rate` 有界退避（`softStreak` 翻倍、封顶 `soft_rate_max`） | 不再写账号级 `until` |
| 11140 rate-limiting（非 6004） | 账号 | 同既有语义 | 账号级限流不产生切模型豁免 |
| 14003 模型繁忙 | `(账号, 模型)` | 10s 起、封顶 1min；**带 `Retry-After` 时取该值并仍钳到 1min** | 采信响应头但不允许超长挂起 |
| 11102 / 11158 | `(账号, 模型)` | 6h 起指数、封顶 24h | 不改 |

"模型级"一律写 `modelCooldowns[model]`，账号级 `until` 保持不动——这是"切模型即可用"
的实现基础，也是 R2-1 的全部收益来源。

### C6 按域的模型阻塞判定

`Pool.ModelBlocked(model)` → `Pool.ModelBlockedForRealm(realm, model)`；原函数保留为
`ModelBlockedForRealm("", model)` 的薄封装（realm 为空 = 全池，既有语义）。realm 谓词
与选号完全一致（同一 `inScope` 判定），否则"选号按域、判定按全池"的错位会复现。

### C7 14003 短等待

在轮转循环的"选不到号"分支：

- 仅当 `modelBlock.Blocked` 且 reason 是瞬时（`modelBlockIsTransient`，14003）；
- 且 `Until` 在 `maxModelBusyWait = 5s` 之内；
- 且本次请求尚未等待过；
- 则 `select { <-ctx.Done() / <-time.After(remaining) }` 后再试一次。

不满足任一条件立即按原逻辑 `break`（不浪费一次请求的时间）。次数上限为 1，取消
由 `r.Context()` 承担。

### C8 落盘

- `dirty` 只在**成功后**清：`saveLocked()` 返回成功才 `dirty.Store(false)`；
  失败保持脏，由下一轮 flusher 重试（既有节流日志不变）。
- 写入序列：`tmp` → `Write` → `Sync` → `Close` → `Rename` → 目录 `Sync`。
- 成功后把上一份主文件轮转为 `state.json.bak`（rename 前先 copy 旧主文件，避免
  rename 覆盖后无法回滚）。
- `load()`：主文件解析失败 → 尝试 `.bak`；成功则采用并把坏文件改名
  `state.json.corrupt`（保留证据，且不阻塞下次启动）。

### C9 预算热改

保存配置路径在写完文件后、返回 `restartRequiredFields` 之前调用
`h.SetBudgetLimit` / `h.SetBudgetGuards`；对应三项从 `restartRequiredFields` 移除。
`restartRequiredFields` 仍是"整类清单"，因此移除即表示对这三个键不再需要重启。

### C10 过期时间归一

`parseExpiresAt(v int64) int64`：

- `v <= 0` → 0（保持"无到期"语义）；
- `v > 1e11` → 视为毫秒，`v / 1000`（`1e11` 秒 ≈ 5138 年，任何真实秒值都小于它）；
- 否则原样返回。

对秒值幂等。写回仍为秒，配置文件格式不变。

## 取舍

- **不新增配置项**：三个改动集都不引入新键，避免热改语义与默认值扩散。14003 的
  等待上限用常量（5s），与既有 `modelBusyMax` 同处定义。
- **6004 模型级不封顶，但保留脏值钳制**：完全信任上游会让一条错误文案把模型停到
  遥不可及的未来；只钳"过去"和"超过 7 天"两种明显异常，其余原样遵守。
- **备份文件不清理**：固定单份 `.bak`，由每次成功落盘覆盖，不产生增长问题。
- **不改 `applyErrorPolicy` 签名**：10+ 调用点，改签名收益低、回归面大；网关 401
  的差异化处置在轮转循环内用局部变量/独立 case 表达。

## 兼容性

- 配置文件：无新增键；`budget.*` 从"需重启"变"即时生效"是行为改进，配置内容不变。
- `state.json`：结构不变，新增 `.bak`/`.corrupt` 旁路文件；旧版本忽略它们。
- 凭据文件：结构不变；毫秒输入在解析时归一，写回仍是秒。
- 对外契约：HTTP 状态码、错误 `code`、`Retry-After` 语义均不变。

## 回滚

- ①：`git revert` 该提交即可，三处改动互不依赖。
- ②：同上；若只想回退"短等待"，删 `maxModelBusyWait` 分支即可。
- ③：同上；`.bak`/`.corrupt` 文件可保留（无害）或手工删除。
