# 实施计划：同步上游第二轮风控修复

## 环境

- Go 不在 PATH 上，固定用 `C:\tools\go\bin\go.exe`（go1.27.3 windows/amd64）。
- 不向真实账号发请求：所有测试只打 `testClient` / `roundTripFunc` 假上游。

```powershell
$go = 'C:\tools\go\bin\go.exe'
& $go build ./...
& $go vet ./...
& $go test ./...
```

## 变更集 ① 刷新与误禁保护（P0）— 完成

- [x] 1.1 `refresh_singleflight.go`：`releaseRefresh`（发布 + 唤醒）、
      `refreshResult.applyTo`（带 RT 守卫）；`refreshCoalesced` 改 defer 发布，
      panic 时预置 `refresh aborted` 并先释放等待者再抛。
- [x] 1.2 `auth.go`：`ExpiresAtValue()` 加锁读取。
- [x] 1.3 `scheduler.go` 保活：`ErrSessionDead` 且 `SessionDeadButUsable` → 不计 12153。
- [x] 1.4 `handler.go` 预刷新：AT 仍可用 → 不罚不禁、用现有 AT 走完这一跳。
- [x] 1.5 `handler.go` 轮转循环加 `rotate:` 标签；401 求证 `switch` 内两处 `break`
      改 `break rotate`（修 ctx 取消落回循环外、二次罚号/误禁）。
- [x] 1.6 `handler.go` 新增 `case VerifyDead`：走 `NoteSessionDead` 连续阈值。
- [x] 1.7 测试：
      - `refresh_singleflight_test.go`：失败传递（去掉发布语句必红）、宽限复用应用凭据、
        panic 释放等待者且 inflight 清空。
      - `handler_refresh_test.go`：预刷新 RT 失效 + AT 有效 → 不禁号；AT 过期 → 禁用；
        网关 401 判死连续 3 次才禁用。
      - `scheduler_test.go`：AT 有效时保活不计死（新增）；
        `TestRunKeepaliveSessionDeadResetBySuccess` 调整为显式拨回过期 AT（原断言靠
        刷新成功后自带的有效 AT 成立，语义已变）。

## 变更集 ② 模型限流、按域阻塞与短等待（P0/P1）— 完成

- [x] 2.1 `cooldown.go`：`CooldownSoftForModel` 改为恒定模型级作用域；明确重置时间
      **不再被 `soft_rate_max` 截短**（`modelSoftUntilLocked` 只钳过去/超 7 天）。
- [x] 2.2 `cooldown.go`：`CooldownModelBusyWithWait` —— 14003 带 `Retry-After` 时采信，
      钳到 `modelBusyMax`，不缩短已有更晚截止。
- [x] 2.3 `handler.go` `applyErrorPolicy`：6004 三个分支全走模型级；14003 透传
      `uerr.RetryAfter`；`RecordModelRateLimitAudit` 不再被 6004 调用。
- [x] 2.4 `cooldown.go`：`ModelBlockedForRealm(realm, model)`；`ModelBlocked` 保留为
      全池薄封装；`handler.go` 选号失败处按 `realm` 判定。
- [x] 2.5 `handler.go`：14003 瞬时阻塞短等待（`maxModelBusyWait=5s`、1 次、ctx 可取消，
      复用 `backoff.go` 的 `sleepCtx`）。
- [x] 2.6 测试：`modelrealm_test.go`（域隔离 / 空域不误报 / Retry-After /
      不缩短更晚截止）；`modelcooldown_test.go`、`pool_test.go`、`handler_test.go`
      的旧契约断言改写为新契约。

## 变更集 ③ 可靠落盘与预算热改（P0/P1）— 完成

- [x] 3.1 `persist.go`：`flushLocked()` —— 脏才写、**写成功才清脏**；`saveLocked` 返回 error。
- [x] 3.2 `persist.go`：`writeFileSync` / `copyFileSync` / `syncDir`；成功轮转 `.bak`；
      `load()` 主文件损坏 → `.bak` 顶替 + 坏文件改名 `.corrupt`。
- [x] 3.3 `auth.go`：`saveFileAtomicRetry`（3 次、20ms 起退避、fsync）；
      `normalizeExpiresAt`（毫秒→秒，两处解析分支接入）。
- [x] 3.4 `main.go`：`budgetHook` 接线 `SetBudgetLimit` / `SetBudgetGuards`；
      `budget.*` 从 `restartRequiredFields` 移除。
- [x] 3.5 测试：`persist_recovery_test.go`（失败保脏 / `.bak` 恢复 / 缺主文件空池 /
      成功轮转 `.bak`）；`auth/expires_test.go`（毫秒归一 / Parse 接入 / 落盘无残留 tmp）；
      `budget_hot_test.go`（重启清单不含 budget.*）。

## 全量验证

```powershell
& $go build ./...   # ok
& $go vet ./...     # ok
& $go test ./...    # 全部 ok
```

- [x] 三个改动集在同一工作树内各自可独立 revert（改动文件基本不重叠）。
- [x] `git diff` 自查：无调试残留、无无关格式化、无新增配置键。
- [x] spec 同步：`error-handling.md`（处置矩阵 / 登录态验证 / 反模式）、
      `pool-lifecycle.md`（冷却矩阵 / 落盘语义 / 反模式）、
      `config-and-hot-reload.md`（budget 热生效）。

## 提交边界（建议三个提交）

1. `fix(upstream): 刷新单飞发布结果，RT 失效但 AT 有效不再误禁`
2. `fix(pool): 模型级限流只停该模型，按域判断阻塞并短等待 14003`
3. `fix(persist): 落盘失败保留 dirty，凭据保存重试与预算热改`

## 已知偏离

- 1.1–1.6 的代码在任务建立**之前**已落盘（流程违规，已在会话中说明）；
  已纳入上面的测试与自查，未回滚。
- R2-5 短等待只有实现 + 常量约束，没有独立测试：可断言的只有"等或不等"的分支，
  时间驱动的断言会引入 sleep 抖动（`quality-and-testing.md` 明确不用假时钟）。
  已用「cap 5s + 只等一次 + ctx 取消」把风险面压到最小，留待人工复核。
- 第二批候选（保活刷新窗口 / 内容审核分类 / 排程 CST + 任务级抖动）未纳入本任务，
  按 prd 的非目标处理。
