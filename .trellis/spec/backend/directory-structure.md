---
name: backend-directory-structure
description: 包职责、真实 import 方向、包内按关注点拆文件的约定、新代码落点判断
paths:
  - cmd/**
  - internal/**
---

# 目录结构与分层

## 顶层布局

```
cmd/
  server/        网关主程序（唯一带 flag 的入口）+ config.go（配置模型）+ wiring.go
  login/         OAuth 设备码登录 CLI（url / poll / realm）
  credit/        余额报表 CLI（JSON 或 -pretty）
  signin/        批量签到 CLI（会写回 auths/*.json）
  trial/         global 域试用包领取 CLI
internal/
  server/        OpenAI 兼容 HTTP 层：路由、鉴权、轮换循环、响应整形、每请求日志
  upstream/      上游协议层：payload 改写、SSE、DSML、出站头、脱敏、错误分类、各业务端点
  pool/          账号池：状态机、选号、冷却、落盘、Redis 镜像
  scheduler/     六类定时任务（checkin/travel/activity/keepalive/blackcat/growth）
  session/       会话粘性路由 + 各类 ID 生成
  panel/         管理面板：Go HTTP 编排 + go:embed 的 index.html / app.js
  auth/          凭证模型与登录态（文件读写、realm、刷新判定）
  apikeys/       多 API key 存储、模型白/黑名单、脱敏、指纹
  usage/         用量账本（小时/天桶、滚动聚合、落盘）
  reqlog/        请求归档（JSONL）与 Request-ID
  logfmt/        日志格式化原语（UID8、Label、ShortUA、Truncate）
  prompt/        系统提示词三种模式与改写/补位
  livecfg/       热生效配置的原子快照
  httpauth/      网关与面板共用的常量时间 Bearer 校验
  redisstore/    Upstash Redis 客户端（可选，失败即 Noop）
scripts/         Python 探测/任务脚本 + docker.sh
auths/           运行期账号凭证（明文 token，gitignore）——不是代码目录
data/            运行期状态（usage.json、request-logs、model.json，gitignore）
```

## 依赖方向（实测 import 图，不要反向）

```
leaf（无 internal 依赖）
  logfmt, httpauth, livecfg, reqlog, prompt, apikeys, usage, redisstore

auth       → logfmt
session    → redisstore
pool       → auth, logfmt
upstream   → auth, logfmt, session
scheduler  → auth, logfmt, pool, upstream
server     → apikeys, auth, httpauth, livecfg, logfmt, pool, prompt, redisstore,
             reqlog, session, upstream, usage
panel      → apikeys, auth, httpauth, livecfg, logfmt, pool, reqlog,
             scheduler, upstream, usage
cmd/*      → 上述任意
```

由此得到三条硬规则：

1. **`pool` 不认识 `upstream`。** 模型限流查询是 `cmd/server` 注入的闭包（`cmd/server/main.go` 中 `p.SetModelRateOf(...)`，位置在 `upstream.New()` 之后），`pool` 只持有函数值。池子永远不能自己发网络请求。
2. **`upstream` 不做换号决策。** 它返回带类别的 `*upstream.Error`；"换号 / 罚号 / 直接返回"由 `internal/server` 的轮换循环决定。
3. **`panel` 不复制池语义。** 面板的账号操作必须调用 `pool` 的既有方法（`panel.go` 顶部注释明确禁止改写池语义）。

第三方依赖只有 `internal/redisstore` 直接 import `github.com/redis/go-redis/v9`；`go.uber.org/atomic`、`golang.org/x/sys` 都是它的间接依赖。其余全部 stdlib。新增第三方库要先证明 stdlib 做不到。

## 包内拆文件约定

大包按**关注点**拆成多个小文件，而不是一个大文件堆所有方法。新代码照既有形状放：

| 包 | 现有拆分 | 新逻辑该进哪个文件 |
|----|----------|--------------------|
| `internal/server` | `handler.go`（路由+轮换）`logging.go`（每请求行/用量）`backoff.go`（退避+jitter）`degrade.go`（降级窗口）`wafip.go`（WAF IP 闸）`resolve_model.go`（realm 前缀）`rc.go`（读关闭器） | 新的"请求级闸门"仿 `degrade.go`/`wafip.go`；新的退避计算只能进 `backoff.go` |
| `internal/upstream` | 一个关注点一个文件：`payload.go` `sse.go` `headers.go` `sanitize.go` `thinking.go` `dsml.go` `tool_pairing.go` `usage.go` `cache_key.go` `transport.go`；业务端点按域拆 `report.go` `tasks.go` `travel.go` `streak.go` `school.go` `desktop.go` `blackcat.go` `trial.go` | 新的协议改写进对应关注点文件；新上游端点按域新建 `xxx.go`，方法挂在 `Client` 上 |
| `internal/pool` | `entry.go`（数据结构）`state.go`（对外原语）`transition.go`（内部状态原语）`pick.go`（选号）`cooldown.go`（冷却）`persist.go`（落盘/镜像）`modelview.go` `realm.go` `degrade.go` | 改状态字段的代码只能进 `transition.go`；对外方法进 `state.go`；不要把状态字段赋值散到新文件 |
| `internal/panel` | Go 一个关注点一个文件（`panel.go` `apikeys.go` `config.go` `tasks.go` `taskcenter.go` `autotask.go` `batch.go` `login.go` `import.go` `ring.go` `index.go`），前端固定 `index.html` + `app.js` | 新面板 API 复用同域 `*.go`；前端改动落在两个内嵌资产里（见 [../frontend/panel-ui.md](../frontend/panel-ui.md)） |

约定细节：

- 测试文件与被测文件**同包**、同目录，命名 `<file>_test.go`。既有测试直接用内部包名（`package server`、`package pool`），以便测未导出符号。跨文件共享的测试夹具放该包某个 `_test.go` 顶部（如 `handler_test.go` 的 `sseOK`）。
- 只有 `cmd/server` 用 `flag`；其他 CLI 手写位置参数解析（`cmd/login` 自解析 `--realm`）。
- 包级常量集中在使用它的文件顶部，注释写"为什么是这个值"与"不能改的理由"（例：`upstream/headers.go` 的 UA 版本常量、`server/handler.go` 的 `notFoundCooldown`）。

## 新代码落点判断

| 你要改的东西 | 落点 |
|--------------|------|
| OpenAI 请求/响应形状、状态码、`X-Request-Id` | `internal/server/handler.go` |
| 出站请求体怎么改（字段归一、思维链、工具配对） | `internal/upstream/payload.go` 及其子步骤文件 |
| 上游流怎么解析/重建 | `internal/upstream/sse.go` |
| 上游错误码含义 | `internal/upstream/client.go` 的 `Classify` + `hint.go` |
| 罚号/冷却时长 | `internal/pool/cooldown.go`（档位表见 [pool-lifecycle.md](./pool-lifecycle.md)） |
| 选哪个号 | `internal/pool/pick.go` |
| 定时任务跑什么 | `internal/scheduler/<域>.go` |
| 配置字段 | `cmd/server/config.go` + `config.example.json` + `README.md` 字段表（三处都要动） |
| 面板接口 | `internal/panel/*.go`；对应 UI 在 `internal/panel/app.js` |
| 用量统计口径 | `internal/usage/usage.go`（面板只做查询编排） |

## 反模式

- 在 `internal/server` 里另写一套 SSE 解析或第二个 jitter 实现（`backoff.go` 头注释明确禁止第二份 jitter）。
- 在 `internal/panel` 直接改 `pool` 内部字段，或绕过 `pool` 方法操作账号。
- 为方便测试把业务逻辑塞进 `cmd/server/main.go`：装配文件只做装配与注入，逻辑回各自包。
- 新建 `internal/utils` 大杂烩包——本仓库没有这种包，工具函数就近放在使用它的包（如 `logfmt`）。
