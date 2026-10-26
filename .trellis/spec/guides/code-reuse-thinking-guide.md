---
name: guides-code-reuse
description: 唯一实现清单、有意保留的双实现、写新代码前的四问
---

# 代码复用指南

> 本项目把"同一个语义只能有一份实现"当成硬要求：历史上重复实现导致的漂移事故（鉴权、jitter、tok/s、脱敏）都写进了代码注释。写新代码前先在这份清单里查一遍。

---

## 1. 已经有唯一实现的东西（禁止重写）

| 语义 | 唯一实现 | 重复实现的后果 |
|------|----------|----------------|
| Bearer 常量时间校验 | `internal/httpauth.VerifyBearer` | 曾有两份"字符串直接比较"，既漂移又带计时侧信道 |
| 退避与 jitter | `internal/server/backoff.go`（`backoffAfter` / `jitterDur` / `sleepCtx`） | 两套随机退避会让 ops 日志无法比较 |
| token 速率 | `tokensPerSecond`（`internal/server/logging.go`，被用量账本复用） | 流水行与面板数字对不上（issue #34） |
| uid / 账号标签 / UA / 截断 | `internal/logfmt`（`UID8` / `Label` / `ShortUA` / `Pad` / `Truncate`） | 各处自截断会出现不同宽度与泄漏风险 |
| 密钥脱敏与指纹 | `internal/apikeys.Redact` / `Fingerprint` | 面板显示与会话日志不一致 |
| 模型白/黑名单判定 | `internal/apikeys.ModelFilter.Allows` | 用展示名匹配会永久不命中（已有注释警告） |
| realm 归一（密钥） | `internal/apikeys.NormalizeRealm` | 非法值静默回落会破坏计费隔离边界 |
| realm 归一（排程作用域） | `internal/scheduler.normalizeRealm` | 拼错 realm 变成"一个号都没跑" |
| 状态字段写入 | `internal/pool/transition.go` + `Cooldown*` / `NoteSuccess` / `NoteError` / `NoteFailures` | 散写字段会绕过"不叠加冷却"等不变量 |
| 账号选择 | `internal/pool/pick.go` | 别处再实现一遍权重/地板/LRU 必然分叉 |
| 上游错误分类 | `internal/upstream/client.go` 的 `Classify` | 在 handler/panel 里重判错误码文本会分叉处置 |
| 错误提示文案 | `internal/upstream/hint.go` 的 `GatewayHint` / `FrameHintFunc` | 多处拼装会导致同一错误两种解释 |
| SSE 帧重建 | `internal/upstream/sse.go` | 第二份 SSE 解析器是本项目明令禁止的 |
| 工具标记还原 | `internal/upstream/dsml.go`（`NewMarkupRepair` / `RepairAggregatedResponse`） | 在 server 里自己解析标记会破坏"绝不吞字节" |
| 提示词改写 | `internal/prompt`（`Rewrite` / `Append` / `EnsureSystemFirst` / `Degraded`） | 各处自己拼 system 消息会破坏上游 11-128 兼容 |
| 会话键与 ID | `internal/session`（`ExtractKey` / `TurnKey` / `NewMessageID` / `NewTraceID` 等） | 形态不一致本身就是可判别指纹 |
| 用量聚合 | `internal/usage`（`SnapshotWindow` / `Rollup`） | 面板重算会与账本对不上 |
| 配置解析与校验 | `cmd/server` 的 `Default` / `Load` / `ParseConfig` / `normalize` | 面板另走一条解析链会漂移 |
| 面板配置表单映射 | `app.js` 的 `CFG_MAP` + `dig`/`put` | 手写取值/赋值会静默丢字段 |
| 面板复制 | `app.js` 的 `copyText` | 非 secure context 下静默失效 |
| 面板 HTML 转义 | `app.js` 的 `esc` | `innerHTML` 不转义引号 → 属性注入 |

---

## 2. 有意保留的"两处实现"（必须成对维护）

有些重复是刻意的，动一处就必须动另一处，**并在注释里写明原因**：

| 两处 | 为什么保留 | 同步要求 |
|------|------------|----------|
| `internal/server/resolve_model.go` 与 `internal/upstream/payload.go` 的 `SplitRealmPrefix` | 两层都要能识别前缀（入口校验 + 改写剥离），跨包共享会把协议细节漏给 server | 前缀语法（大小写敏感、精确小写枚举）必须一致 |
| `cmd/login` 的 envelope/HTTP 逻辑与 `internal/upstream` | 登录 CLI 要独立发布、不背网关的体积与依赖 | 改认证协议时两处一起看 |
| `cmd/server` 的 `nextMidnightCST` 与 `internal/pool` 的 `nextDay4AM` | 语义不同（降级窗口到零点 vs 硬冷却到 04:00） | **不要合并**；合并会让两种策略互相污染 |
| `scripts/*.py` 与 Go 侧业务实现 | 脚本是开发机排障工具，不参与服务运行 | 不要试图合并到 Go |

判断标准：如果两处是"同一语义的两份代码"，合并；如果两处是"不同语义恰好长得像"，分开并各写注释说明差异。

---

## 3. 写新代码前的四问

1. **仓库里已经有这个语义吗？** 用 `rg` 搜函数名/常量名/中文关键词（本仓库注释写中文，中文搜索往往更快）。
2. **会出现在第二个调用点吗？** 只有一个调用点就就近写在调用方；出现第二个时再抽，抽的时候放到"语义归属"的包（不是 `utils`）。
3. **抽出来的位置是否符合依赖方向？** 见 [../backend/directory-structure.md](../backend/directory-structure.md) 的 import 图——不要为了复用制造反向依赖（例如让 `pool` import `upstream`）。
4. **是否在复制一个"配置默认值"？** 默认值只能在 `cmd/server/config.go` 的 `Default()` 里；别处出现第二份默认值就是 bug 温床。

---

## 4. 反模式

- 建 `internal/utils` / `internal/common` 大杂烩包。
- 把一份逻辑复制过去只改两个常量（应该参数化）。
- 为"复用"把两个语义不同的函数合并（`nextMidnightCST` vs `nextDay4AM`）。
- 在测试里复制生产逻辑来生成期望值（测试就失去意义；期望值应是字面量）。
- 复制粘贴一段带 `TODO` 的代码而不说明差异。
