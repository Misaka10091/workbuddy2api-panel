# Journal - Misaka10091 (Part 1)

> AI development session journal
> Started: 2026-10-26

---

## 2026-10-27 CN 对话主域同步（branch `sync/upstream-cn-chat-domain`，commit `2b7bdd5`）

**做了什么**：按上游 `741cf59` 把 CN 账号的 chat / 模型目录 / growth 域主入口从
`copilot.tencent.com` 切到 `www.workbuddy.cn`，并补 `cn.chat_base` 回填键。

**关键判断**
- `copilot.tencent.com` 是官方 `NetworkFailover` 的备选入口，海外出口 TLS 会复位；
  主入口 `www.workbuddy.cn` 海外可达，所以缺省值才是对的。
- `Client.chatBase()` 是 chat/目录/growth/桌面上报的单一出口，只改这一个字段，
  没有在各端点分别打补丁。
- billing 域（`www.codebuddy.cn`）与 OAuth 登录域（`copilot.tencent.com`）保持不动：
  前者是 CLI 指纹任务计分的判据来源，后者是官方 CLI 口径。
- 分支基于 `main`（不叠在未合并的风控分支上），两个 PR 可独立评审；`client.go`
  改动与风控分支不重叠。

**验证**：`go build ./...`、`go vet ./...` 通过；`go test ./...` 全绿。新增断言覆盖
缺省三域、global 不受影响、`cn.chat_base` 覆盖/解析/重启提示。

**备注**：本地原本没有 `cn` 配置节，本次按上游语义补齐；属装配期字段，
**无条件**列入 `restartRequiredFields`（返回整类清单而非 diff，否则「覆盖值 → 清空」
不会提示）。token 刷新与 chat 共用 `chatBase`，OAuth 设备授权才走 `copilot.tencent.com`。
## 2026-10-27 同步上游风控（branch `sync/upstream-risk-control`，commit `7e18624`）

**做了什么**：逐文件核对上游 `misakano7545/workbuddy2api-panel` HEAD `37b3f4c`，按 P0 出站指纹 /
P1 频控 / P2 状态落盘 / P3 观测与暴露面四组移植，48 文件、+2087/-191。

**关键判断**
- 本地是手工挑选同步分支（分叉点 `dbd7c68`），不能 merge；错误码分类、6004 冷却、401 求证、
  首帧否决此前已同步，本次不重复。
- `/v3/config` 的 `CodeBuddyIDE/…` UA 已从分发包消失，是最显眼的非官方指纹；换成 WorkBuddy
  两/三段式后目录覆盖面略降，接受。
- 头族剥离只做 chat 域（billing/checkin 无真机证据）；`X-Product` 恒 `SaaS`、产品名走 `X-IDE-*`。
- 白名单执法点按本地路由收敛为 `/v1/models` + chat（无 images/search）。
- 摊开/抖动/夜猫子上限全部「配置 0 = 旧行为」，可热关，不需要回滚代码。

**踩坑**
- 用 PowerShell 批量正则替换 `sanitize_test.go` 把 UTF-8 中文写坏（Set-Content 默认 ANSI），
  已 `git checkout` 回滚改用 edit 工具。教训：中文文件禁止用 shell 字符串批量改写。
- `newAccountSpread` 的最小间隔只对「按计划排序后」的相邻账号成立，测试需用 `plan.order()`，
  不能用 uid 顺序断言。

**验证**：`go build ./...`、`go vet ./...` 通过；`go test ./...` 16 包全绿；新增 8 组回归测试。

**遗留**：`X-API-Key` 双头与 images/search 白名单在本地不适用；CN 对话主域改
`www.workbuddy.cn`（上游 `741cf59`）涉及路由变更，待单独决策。


## Session 1: 同步上游第二轮风控修复（误禁保护/模型级限流/可靠落盘）
<!-- trellis-session: v=2 fp=89810f5939497478 -->

**Date**: 2026-10-28
**Task**: 同步上游第二轮风控修复（误禁保护/模型级限流/可靠落盘）
**Branch**: `sync/upstream-risk-r2`

### Summary

按上游 5367e78 修三组缺陷：刷新单飞此前不发布 call.res，失败被等待者读成成功；保活/预刷新未接入 SessionDeadButUsable，RT 死但 AT 有效也会被判死；网关 401 求证里裸 break 只跳出 switch，ctx 取消会二次罚号（VerifyAlive 误禁刚刷新成功的号）。6004 作用域统一到 (账号,模型) 且模型重置时间不再被 soft_rate_max 截短；14003 采信 Retry-After；全池阻塞判定改按 realm。落盘改为写成功才清 dirty，加 fsync/.bak/.corrupt 恢复与凭据保存重试，budget 热生效。三项与本地 spec 冲突，经确认按上游改并同步更新 error-handling/pool-lifecycle/config-and-hot-reload。build/vet/test 全绿。

### Git Commits

| Hash | Message |
|------|---------|
| `7479b5e` | fix(upstream,pool,server): 刷新单飞发布结果、误禁保护与模型级限流作用域 |
| `d3027c9` | chore(trellis): 记录 10-27 风控二轮任务的分支与状态 |

### Status

[OK] **Completed**


## Session 2: 同步上游 P0：出站契约 5.9.3 / 11-128 抢救 / 6020-6021 排队
<!-- trellis-session: v=2 fp=1044e7c9da0fa003 -->

**Date**: 2026-10-29
**Task**: 同步上游 P0：出站契约 5.9.3 / 11-128 抢救 / 6020-6021 排队
**Branch**: `sync/upstream-p0-contract`

### Summary

把上游 5367e78..80e844f 的三项 P0 改动移植到本仓，按三个可独立回滚的提交落地：①出站契约对齐 WorkBuddy 5.9.3（删客户端从不发的头、补必发的头、版本 5.9.3/CLI 2.165.0、ID 改 UUIDv7、report 改走 chatBase、chat body 按 1KB gzip，新增五条路径黄金头集回归）；②11-128 抢救（DSH @ 路径段整段改写、身份句式整句中性化、enum 补推断类型、三级抢救改成拆句优先，并修正三级抢救吃掉 MaxRotate 的回归）；③6020/6021 请求内轮转 + 429+Retry-After（零惩罚保留）、11102 负缓存 TTL 6h/24h 收窄到 2m/10m 并钳制存量条目。四个 spec 同批同步。go build/vet/test 全绿（基线同样全绿）。

### Git Commits

| Hash | Message |
|------|---------|
| `17f7d86` | fix(upstream): 出站契约对齐 WorkBuddy 5.9.3 |
| `27396a0` | fix(upstream,prompt,server): 11-128 抢救——@ 路径段/身份句式/system 拆句 |
| `562442e` | fix(server,pool): 6020/6021 请求内轮转 + 429；11102 负缓存 TTL 收窄 |
| `304f92e` | chore(trellis): 记录上游 P0 同步任务（prd/design/implement/research） |

### Status

[OK] **Completed**
