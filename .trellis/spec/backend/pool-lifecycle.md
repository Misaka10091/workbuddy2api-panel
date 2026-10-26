---
name: backend-pool-lifecycle
description: 账号池状态模型、锁纪律、选号算法、冷却矩阵、落盘与 Redis 镜像、会话粘性、定时排程
paths:
  - internal/pool/**
  - internal/session/**
  - internal/scheduler/**
  - internal/redisstore/**
---

# 账号池、会话与排程

`internal/pool` 是账号状态的**唯一**权威。任何"这个号现在什么状态"的判断都在这里；任何"该罚多重"的决定也都在这里。

## 状态模型：没有枚举，只有四个正交维度

```go
// internal/pool/entry.go
type entry struct {
    a *auth.Auth
    credits, creditsTotal          // 余额
    creditsExpiring, creditsEarliestExpiry, creditsEarliestRemaining  // 近过期积分
    successCount, errTotal, lastErr, lastSuccess
    coolKind, until                // 账号级冷却（CoolSoft / CoolHard）
    disabled, paused, reason
    lastUsed, usedSeq              // 运行期：防集中
    breakerUntil, fails, retryCount
    softStreak                     // 独立于 fails 的"退避档位"计数
    modelCooldowns map[string]modelCooldown   // 模型级冷却
    sessionDeadFails, consecutiveFails, degradeUntil
    modelCost map[string]costObs   // 实测成本（tier 判定用）
    inFlight atomic.Int64          // 唯一的 atomic 字段
}
```

派生谓词而非状态字段：

- `healthy` = 未 `disabled`、未 `paused`，且 `until` / `breakerUntil` / `degradeUntil` 都不在未来（三者是 OR 门，取最长即自然生效，不需要手写 max）。
- `healthyForModel` = `healthy` 且该模型不在 `modelCooldowns`（`AuditOnly` 条目**必须**被忽略，否则会改动选号行为）。
- `Status.Cooling` = `until` 或 `breakerUntil` 在将来。

### 锁纪律

- `p.mu sync.RWMutex` 保护 `byUID` 与**所有非 atomic** 字段。只读查询用 `RLock`，任何写入用 `Lock`。
- `inFlight` 用 atomic，就是为了让 `Pick` 热路径不必拿写锁。
- 需要写锁的内部原语一律带 `…Locked` 后缀（`disableLocked`、`reviveCoolingLocked`、`pickEarliestExpiryLocked`、`saveLocked`…），doc 注释写"调用方必须已持有 p.mu"。新加同类函数照此命名。
- **锁内不做网络 I/O**。池子本身不发请求；需要动 `auth.Auth` 时先放掉 `p.mu` 再拿 `a.Lock()`（`cooldown.go` 的 `CooldownSoftRate` 就是先 `RUnlock` 再操作）。
- 注入的回调（`SetModelRateOf` 等）**不得反向调用 Pool 方法**，会造成死锁（`pool.go` 注释钉住）。

### 状态转移入口

所有字段写入只能经 `transition.go` 的内部原语，或经 `Cooldown*` / `NoteError` / `NoteSuccess` / `NoteFailures` 等封装。矩阵（`transition.go` 头注释）：

```
disabled          ← disableLocked（Disable / NoteSessionDead 达阈值）
until + coolKind  ← Cooldown / CooldownSoftRate
modelCooldowns    ← CooldownSoftForModel / CooldownModelBusy / BlockModelBackoff
breakerUntil      ← recordBreakerFailureLocked
softStreak        ← CooldownSoftRate / CooldownSoftForModel
sessionDeadFails  ← NoteSessionDead
consecutiveFails  ← NoteError / NoteFailures
```

语义细节（都踩过坑）：

- `disableLocked` 会清冷却但**保留 breaker**。
- `reviveCoolingLocked` 只解 `CoolHard` 与余额，**不能**清 `CoolSoft` / `softStreak` / `modelCooldowns` / breaker（清了等于全池冷却保护形同虚设）。
- `NoteSuccess` 清失败计数、breaker、streak、degrade、sessionDead，**不碰** `until` / `modelCooldowns`。

## 选号（`pick.go`）

```
weight = 1.0 + credits/maxCredits*10 + idleWeight
idleWeight = lastUsed.IsZero ? idleWeightMax : clamp(小时数*idleWeightPerHour, 0, idleWeightMax)
routing  = weight * (近过期 && prefer_expiring ? 3 : 1)
```

- 有 `1.0` 基数，所以全零余额的池也能加权。
- 已**移除**历史上的 `successRate × 3` 因子：`errTotal` 是终身累计，早期错误会永久惩罚。
- 负的 idle（时钟回拨）钳 0。
- `maxCredits` 用**过滤前**的健康集合算，保证排序与抽签同一基准。

成本分层：`costTier` 0=实测免费、1=无观测/过期、2=实测付费。对候选做**硬过滤**到最好档，而不是仅排序。条件性探索（`cost_explore_interval`）：当最优档是 0 且存在 tier-1 候选、且距上次探索超过间隔，本次改用 tier-1 档，键是 `realm+"\x1f"+model`。

- 截断 Top-5 后加权抽签；等权时先做一次 shuffle，**但必须用自带 PCG 源，不能消耗 `p.randInt64N`**（否则会破坏注入式确定性测试）。
- 比较器：`cost1k` 升 → weight 降 → UID 升；权重预先算好，别放进比较器里重算。
- 防集中：`minPickGap = 100ms`，太近用过的不进候选；候选为空时按 `usedSeq` 取 LRU（不用墙上时钟）。**LRU/fallback 命中必须推进 `pickSeq`/`usedSeq`**，否则它永远是最"旧"的，会被连续选中。
- 排除顺序：`tried` → `healthyOf` → 积分地板 → 在途已满。
- 积分地板（`credit_floor`）：仅当"有地板 + 有具体模型 + 余额低于地板"才判；优先用实测 `CostPer1k`，否则查模型目录费率；**目录没覆盖就放行**（未知不拦）。
- 兜底 `pickEarliestExpiryLocked`：跳过 `tried` / 错 realm / 停用暂停 / 生效中的硬冷却 / 地板拦截 / 在途满 / 无到期时间，取最早恢复者，并记 `WARN ... fallback_earliest_expiry`。

## 冷却矩阵（时长从哪来）

| 触发 | 入口 | 时长 | 配置键 |
|------|------|------|--------|
| 429 无重置时间 | `CooldownSoftRate` | `d << (streak-1)`，封顶 `soft_rate_max` | `cooldown.soft_rate` / `cooldown.soft_rate_max` |
| 429 有重置时间 | 同上 | `min(上游重置时刻, now+soft_rate_max)`，**绝不指数堆加** | 同上（只取上界） |
| 6004/6000 模型限额 | `CooldownSoftForModel` | 只写 `modelCooldowns[model]`，保留上游墙钟 | `soft_rate_max` 仅作上界 |
| 14003 模型忙 | `CooldownModelBusy` | 10s 起 ×2，封顶 1m | 硬编码 |
| 402 余额耗尽 | `CooldownUntilTomorrow4AM` | 到本地 04:00（04:00 前则今天 04:00） | 硬编码 |
| 404 | handler 调 `Cooldown(CoolSoft, 60s)` | 固定 60s，不推进 streak | 硬编码（有意不复用 `soft_rate`） |
| 11102/11158 | `BlockModelBackoff` | 6h 起 ×2，封顶 24h | 硬编码 |
| 5xx 熔断 | `recordBreakerFailureLocked` | `breaker_cooldown << retryCount`，封顶 `breaker_cooldown_max` | `pool.breaker_threshold` / `breaker_cooldown` / `breaker_cooldown_max` |
| 未分类失败 | `NoteFailures` | 固定 `degrade_cooldown`，封顶 `degrade_cooldown_max` | `pool.degrade_threshold` / `degrade_cooldown` / `degrade_cooldown_max` |
| 登录态连续失效 | `NoteSessionDead` | 连续 3 次 → disable | 硬编码阈值 |
| 在途 | `Acquire`/`Release` | 租约，`0 = 不限` | `pool.max_in_flight` / `pool.max_in_flight_global` |

铁律：**已在冷却中的重复失败不推进 streak、不延长到期**（`cooldown.go` / `degrade.go` 注释均钉住）。`softDurationLocked` 用 `softStreakShiftMax=16` 防位移溢出。

## 落盘与 Redis 镜像（`persist.go`）

- 文件格式 `stateFile{accounts: map[uid]stateAccount}`，`MarshalIndent` 两空格、`0o600`、`tmp` + `os.Rename` 原子发布，目录 `0o755`。
- 周期 5s（`flushInterval`）后台 flush；`Flush()` 用 `dirty.Swap(false)` 幂等；`Close()` 停 goroutine 并做最后一次 flush。flusher 启动前必须把 `stopCh` 抓到局部变量（`-race` 与 nil channel 死锁的教训）。
- **落盘字段规则**：
  - 截止时间用 `*time.Time` + `omitempty`，并且**只在仍在将来时写**；恢复时同样过滤。非指针 `time.Time` 会被序列化成 `0001-01-01T00:00:00Z`。
  - 计数器（`err_total` / `success_count` 等）**不要** `omitempty`：零值缺失会让人误以为"没记录"。
  - 缺失字段按零值解码，即向后兼容；`err_count` 是历史键，读时迁移为 `max(err_total, err_count)`。
  - 恢复时做净化：`creditsExpiring`/`earliestRemaining` 钳进 `[0, credits]`，过期项丢弃；负 `CostPer1k`、零 `LastSeen` 丢弃。
- **运行期字段不落盘**：`lastUsed`、`usedSeq`、`inFlight`、`fails`、`exploreLast`、`costExploreEvents`、`persistFails`、`pickSeq`、`modelCooldown.Hits`（"从 6h 基数重新学习"）。
- 落盘失败不冒泡：`notePersistFail` 首次记日志，之后每 12 次记一次；恢复时记一行并清零计数器。
- Redis 镜像：`StoreSnapshotter{SaveState([]byte); LoadState() ([]byte,bool)}` 这个**窄接口**是池子唯一依赖。`saveLocked` 尾部异步提交，Redis 错误只记 debug、永不返回。key 为 `wb2api:state` / `wb2api:bind:<key>`，bind TTL 7 天。
- 启动恢复顺序：`RestoreFromSnapshot` 必须**先于** `SyncToDir`。快照 `saved_at` 为零/不可解析或没有快照 → 本地文件优先；本地文件不可读，或快照不比本地旧 → 采用快照并置 `dirty` 以便落盘。

## 会话粘性（`internal/session`）

- 键优先序：`metadata.conversation_id` → `metadata.conversationId` → `conversation_id` → `conversationId` → `prompt_cache_key` → 派生键。**`user_id` 不是粘性键**；一旦出现 `user_id`，派生被抑制（返回空键 → 退化为加权轮转）。
- 派生键 `d-` 前缀 + `sha256(首条 system 文本 + "\x00" + 首条 user 签名)[:16]`；非文本分块只写 `[type]` 占位、**不含内容摘要**（否则图片 URL 轮换就换键）。
- `TTL` 默认 30m、GC 5m；每次命中滚动续期；`LoadFromStore` 只在启动跑且**不覆盖**本地已有绑定（"本地一旦建立即为权威"）。
- 解绑发生在 handler：只有失败的号**就是**当前粘性号时才解绑；粘性号因模型冷却/地板/在途满返回 nil 时也解绑并走普通轮换。
- ID 生成（`ids.go`）：`NewMessageID` 32 hex（`crypto/rand`，回退 `math/rand/v2`）；`NewConversationID` 只在网关自建会话时用 UUIDv4；`NewTraceID`/`NewSpanID` 纯随机、**不带时间位**（形态即指纹）；`RequestIDForKey` 是纯派生不缓存。

## 定时排程（`internal/scheduler`）

- 六类：`checkin` / `travel` / `activity` / `keepalive` / `blackcat` / `growth`。小时与 `*_enabled` 开关来自配置；**开关命名是 `XxxDisabled`**，所以零值 Config 就是"全部启用"。
- `nextFire` 取下一个整点；`nextWake` 返回最早时刻**以及所有同一时刻的 kind**，因此同小时的任务会一起触发；每个 kind 一个 goroutine + `wg.Wait()`，慢任务不阻塞同槽任务。
- 等待按 ≤60s 分段重读**墙上时钟**（Modern Standby 会冻结单调时钟）；迟到唤醒给 5s grace。
- 每任务幂等：
  - 签到：池侧 `lastCheckinDay`；"今天已签到"算成功。
  - 旅行/领养：`adoptTried map[uid]CST 日期`（进程内，重启清零），仅在上游报"任务未完成"时写；上游 `DailyLimitReached` 直接跳过。
  - 行为事件按天幂等。
- realm 作用域：所有对外的 `XxxNow()` 都是 `XxxRealm("")` 的壳；未知 realm **回落全池而不是报错**（拼错导致"一个号都没跑"更难排查）。
- 各任务的域排除：签到跳过 global 但仍查企业余额；活跃只跳企业；旅行/黑猫/连登跳 global + 企业。
- `paused` 只被黑猫跳过（它是唯一发真实对话的任务），其余保号任务对 paused 账号照跑。
- **同槽任务没有账号级互斥**，一个号可能同时被签到与活跃打到；需要串行的场景只能靠"同槽不同 kind 分属不同 goroutine + 上游自身幂等"。

## 测试约定

- 没有假时钟。确定性靠四招：把截止时间**回拨**（`expireCooldown` 直接改 `e.until = now-1s`）、容差断言（`wantCoolSec`）、注入 RNG（`SetRandomSource(func(int64) int64 { return 0 })`）、关掉 `minPickGap`（`withNoPickGap`）。
- 真实 sleep 只用于真正时间驱动的行为，并且用**有上界的轮询**（`TestAutoFlush` 把 `flushInterval` 调到 20ms 后轮询 `os.Stat`）。
- 测试直接用内部包名，读 `p.byUID` 前必须持锁；测试专用访问器写在 `_test.go` 里。
- 破坏性场景用"父路径是普通文件"来稳定触发 `MkdirAll` 失败。

## 反模式

- 在 `internal/scheduler` 或 `internal/server` 直接改 `entry` 字段（绕过 `transition.go`）。
- 给 6004 也写 `until`（会把单模型限流升级成整号冷却）。
- 在 `NoteSuccess` 里顺手清 `modelCooldowns`。
- 让 `reviveCoolingLocked` 清掉 `softStreak` / breaker。
- 给状态文件计数器加 `omitempty`，或给截止时间用非指针 `time.Time`。
- 用 `time.Sleep` 写冷却测试（应该回拨时间或注入 RNG）。
- 在池子里加网络调用（应该由 `cmd/server` 注入闭包）。
