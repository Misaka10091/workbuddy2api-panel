---
name: backend-index
description: Go 服务端规范入口：包地图、前置清单、质量门、最容易违反的硬约定
---

# 后端规范入口

本目录描述 `cmd/` + `internal/` 这**一个** Go module 的真实约定。module 路径 `github.com/linguo2625469/workbuddy2api-panel`，`go.mod` 声明 `go 1.22.5`。

网关做的事：把腾讯 CodeBuddy 账号包装成 OpenAI 兼容 `/v1/chat/completions`，多账号池轮转 + 熔断冷却 + 会话粘性 + 定时保号任务 + 内嵌 Web 面板。理解这一点，才能判断一段改动该落在哪一层。

## 文件地图

| 文件 | 管什么 | 主要管辖源码 |
|------|--------|--------------|
| [directory-structure.md](./directory-structure.md) | 包职责、依赖方向、新代码该放哪 | `cmd/**`、`internal/**` |
| [config-and-hot-reload.md](./config-and-hot-reload.md) | 配置 schema、缺省值、`normalize()`、环境变量、原子落盘、热生效 vs 重启 | `cmd/server/**`、`internal/livecfg/**` |
| [http-api-layer.md](./http-api-layer.md) | 请求管线顺序、错误响应形状、状态码映射、流式/非流式、请求级状态 | `internal/server/**` |
| [upstream-protocol.md](./upstream-protocol.md) | payload 改写顺序、SSE 帧白名单、DSML 工具标记还原、出站头族、脱敏、上游端点清单 | `internal/upstream/**` |
| [pool-lifecycle.md](./pool-lifecycle.md) | 账号状态模型、锁纪律、选号、冷却矩阵、落盘与 Redis 镜像、会话粘性、排程 | `internal/pool/**`、`internal/session/**`、`internal/scheduler/**`、`internal/redisstore/**` |
| [error-handling.md](./error-handling.md) | 错误分类、轮换策略、罚号边界、OpenAI 错误体 | `internal/server/**`、`internal/upstream/client.go`、`internal/pool/**` |
| [logging-and-observability.md](./logging-and-observability.md) | 每请求一行表格日志、JSONL 归档、脱敏、用量账本 | `internal/server/logging.go`、`internal/reqlog/**`、`internal/logfmt/**`、`internal/usage/**` |
| [quality-and-testing.md](./quality-and-testing.md) | 验证命令、测试写法、确定性技巧、回归测试形状、版本纪律 | `*_test.go`、`.github/workflows/**` |
| [cli-and-scripts.md](./cli-and-scripts.md) | 四个 CLI、Python/Shell 辅助脚本、镜像与发布 | `cmd/login/**`、`cmd/credit/**`、`cmd/signin/**`、`cmd/trial/**`、`scripts/**`、`*.sh`、`Dockerfile` |

前端（面板 `index.html` + `app.js`）在 [../frontend/index.md](../frontend/index.md)。

**代码怎么写之外**的那一半——分支、commit message、作者/AI 署名、PR、tag、什么不该提交——在 [../collaboration/index.md](../collaboration/index.md)。提交前务必过一遍那里的检查表（本仓库 `*.md` 被全局忽略、`auths/` 是明文凭证、根目录有运行产物，`git add -A` 会出事）。

## 前置清单（动手前逐条过）

- [ ] 定位改动属于哪一层：装配（`cmd/server`）/ 协议（`internal/upstream`）/ 路由与整形（`internal/server`）/ 账号状态（`internal/pool`）/ 面板（`internal/panel`）。层放错了，代码再对也会被后一轮改回来。
- [ ] 读对应主题文件，并至少读 2 个同类既有实现（本仓库的同构代码很多，先抄形状再动逻辑）。
- [ ] 确认落库方式：分支名、commit type/scope、是否需要 AI `Co-authored-by` 尾注（见 [../collaboration/git-workflow.md](../collaboration/git-workflow.md)）。
- [ ] 判断该配置是**热生效**还是**装配期**：装配期字段改了必须同步 `restartRequiredFields`（见 config 规范）。
- [ ] 判断是否改动对外契约：OpenAI 响应体、`/status` 字段、面板 JSON 字段、`usage.json` / 状态文件 schema。这四类都要求**只增不改**。
- [ ] 判断是否新增会计入惩罚的路径：任何"上游出错"的分支都要先回答"该不该罚号、该不该换号"，答案见 [error-handling.md](./error-handling.md)。
- [ ] 若改动跨层（例如协议层新增错误种类），先按 [../guides/cross-layer-thinking-guide.md](../guides/cross-layer-thinking-guide.md) 把两端的映射写清。

## 质量门（提交前必跑）

```bash
go build ./...
go vet ./...
go test ./...
```

- `go vet` 是本仓库**唯一**的静态检查（无 golangci-lint、无 Makefile、无 `go test -race` 门禁）。
- 本机 `go` 可能不在 PATH，已确认安装在 `C:\tools\go\bin\go.exe`；用它时需临时加入 PATH 或写全路径。
- `internal/panel` 的 JS 测试依赖 `node`：缺 node 会 `t.Skip`，CI 的 ubuntu runner 自带 node 所以会真跑。**本机"绿"不等于 JS 已验证**。
- 发布相关改动（版本号、镜像）另见 [quality-and-testing.md](./quality-and-testing.md) 与 [cli-and-scripts.md](./cli-and-scripts.md)。

## 十条最容易违反的硬约定

1. 配置里要热改的值，必须读 `livecfg` 快照或既有 setter，**不能**在装配时拷进结构体字段（`upstream.user_agent` 曾因此假"已保存"）。
2. `Config.Load` / `ParseConfig` / `normalize()` 是唯一解析路径；面板保存也必须走 `ParseConfig`，不另开旁路。
3. 账号状态的写入口只有 `internal/pool` 的状态原语（`Cooldown*`、`NoteSuccess`、`NoteError`、`disableLocked` 等），别在别的包散写字段。
4. 罚号 / 换号 / fail-fast 必须按类别决定，禁止"所有错误都转一圈号"（会把整池打冷）。
5. 上游 `error.message` 一律原样透传，附加信息只走 `gateway_hint` 平行字段，**不替换 message**。
6. SSE 帧是白名单重建而不是透传；`error` 帧是唯一绕过白名单的例外（否则客户端丢 `code/msg`）。
7. 工具标记还原（DSML）"绝不吞字节"：拿不准就原样回吐。
8. 凭据、API key、完整 uid、提示词与响应正文都不进日志；uid 用 `logfmt.UID8`，key 用 `apikeys.Redact`。
9. 新增状态文件字段：截止时间用 `*time.Time` + `omitempty`，计数器**不要** `omitempty`（零值必须能表达"记录过"）。
10. 任何"看起来无害"的 `cmd/signin`、`login.sh` 执行都会写 `auths/*.json` 甚至重启容器；只读排查不要跑它们。

## 禁止清单

- 禁止引入第三方测试框架 / 断言库 / ORM / 模板引擎：本仓库全 stdlib，测试只用 `testing`。
- 禁止在 `internal/server` 复制上游协议细节（SSE 重建、DSML、脱敏、头族）——那属于 `internal/upstream`。
- 禁止在 `internal/upstream` 里做换号决策——回调节点只在 `internal/server` 的轮换循环里。
- 禁止为 `http.Server` 设全局 `WriteTimeout`：会误杀在途 SSE。
- 禁止用 `os.IsNotExist` 判断被 `fmt.Errorf("%w")` 包装过的哨兵错误（用 `errors.Is`）。
- 禁止提交 `config.json`、`auths/`、`data/`、`wb2api.exe` 等运行产物（见 `.gitignore`）；`auths/` 存的是明文 token。

## 已知文档漂移（别照抄 README）

- `README.md` 写 `prompt.mode` 缺省为 `custom`，**代码与测试是 `passthrough`**（`cmd/server/config.go:287`、`cmd/server/config_test.go`）。以代码 + `config.example.json` 为准。
- `README.md` 有一处称"无预编译 release / 无 tag"，与 `.github/workflows/go-binaries.yml` 的 Release 流程及仓库内已有 tag 矛盾。
- `internal/pool` 少数 doc comment 因重构漂移到了错误函数上方（如 `persist.go` 中 `Acquire`/`load` 附近的注释头）；判断行为以函数体为准。
