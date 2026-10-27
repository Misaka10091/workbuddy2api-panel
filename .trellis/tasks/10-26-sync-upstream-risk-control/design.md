# 设计：上游风控同步

## 边界

| 层 | 文件 | 职责 |
|---|---|---|
| 协议 | `internal/upstream/{headers,desktop,client,global_models,sanitize,payload,school,blackcat}.go` | 出站头族/UA/指纹、脱敏、请求体 |
| 路由 | `internal/server/{handler,logging,budget}.go` | 模型白名单、积分闸、工具观测 |
| 状态 | `internal/pool/state.go`、`internal/session/session.go` | 12153 计数落盘、粘性 CAS |
| 排程 | `internal/scheduler/{scheduler,travel,blackcat,humanize,spread}.go` | 摊开、抖动、夜猫子上限 |
| 面板 | `internal/panel/{panel,app.js,autotask}.go` | 防重入、模型缓存、自证前缀 |
| 装配 | `cmd/server/{config,main}.go`、`internal/livecfg` | 配置项、默认值、热生效 |

## 关键取舍

1. **UA 双路而非三路**：上游把 `CodeBuddyIDE`/`CodeBuddy` 两族删掉，改发 WorkBuddy 三段/两段。
   CN 单路（三段），global 两路（桌面三段 + 单品牌两段）。牺牲一点目录覆盖面，换取
   「头集合里没有分发包不存在的串」。
2. **聊天头剥离只做 chat 域**：`CommonHeaders` 仍给 billing/checkin 用，那层没有真机对照
   证据，不在本次改。剥离集中在 `ChatHeaders`，一处收口。
3. **摊开用绝对计划而非逐账号 sleep**：`newAccountSpread` 计算绝对时刻，遍历按计划排序，
   避免「按 uid 序逐个 sleep」在末尾塌缩成一秒。手动入口 `on=false` 直接跳过。
4. **白名单两点执法**：本地无 images/search 路由，执法点收敛为 `/v1/models` 与 chat 入口；
   列表先在带前缀的原始目录上过滤，再按密钥域拆前缀（否则单域视图丢失 `cn:`/`global:` 区分）。
5. **积分闸进程内、按 CST 日归零**：定位是「止血」不是账本；账本仍在 `usage.json`。
   `add` 只认 `observed=true`，避免把「上游没报 credit」当成免费。
6. **品牌词作用域默认 all**：与上游既有行为一致；`identity` 只对 system/developer 生效，
   其余角色原样透传（用户数据里的同名文本不触发 11-128）。

## 兼容性

- 配置文件只增不改：新键都有零值语义（0/空 = 关闭/不启用），旧配置启动行为不变。
- 归档字段只增：`tools_declared`/`tool_calls` 带 `omitempty`。
- 响应契约不变：白名单拒绝复用 `model_not_allowed` 403；预算拒绝用 `insufficient_quota` 429。

## 回滚

- 头族/UA：单文件回退 `headers.go`/`client.go`/`global_models.go`/`desktop.go`。
- 频控：`schedule.account_spread_minutes=0`、`night_chat_cap=0` 即可恢复旧行为，无需回滚代码。
- 预算/白名单：配置置 0/空即关闭。
