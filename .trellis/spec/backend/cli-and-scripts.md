---
name: backend-cli-and-scripts
description: 四个 CLI 工具的输入输出约定、Python/Shell 辅助脚本、镜像与发布资产
paths:
  - cmd/**
  - scripts/**
  - Dockerfile
  - "*.sh"
  - docker-compose.yml
---

# CLI 与运维脚本

## 四个 CLI（`cmd/`）

| 命令 | 用途 | 入参 | 输出 | 退出码 | 会写盘吗 |
|------|------|------|------|--------|:---:|
| `cmd/server` | 网关主程序 | `-config <path>`（唯一的 flag，默认 `config.json`） | 控制台日志 | 启动失败 `log.Fatalf` | 是（配置/状态/归档） |
| `cmd/login` | OAuth 设备码登录 | 子命令 `url` / `poll` / `realm`，`--realm=cn\|global` | `url` 打**裸 URL** 到 stdout；`poll` 打一行 JSON；`realm` 问句走 stderr、结果走 stdout | 失败 1 | 是（落 `auths/*.json`） |
| `cmd/credit` | 余额报表 | 位置参数：`-pretty`（或默认 JSON） | 单行 JSON 或 4 行可读文本 | **恒 0**（单号失败只体现在 `error` 字段） | 否 |
| `cmd/signin` | 批量签到 | 位置参数：凭证目录（默认 `auths`） | 固定宽度表格 + `total=/ok=/already=/fail=` 汇总 | 无凭证文件时 1 | **是**（刷新后 `SaveAtomic`） |
| `cmd/trial` | global 域领试用包 | 位置参数：凭证目录（默认 `auths`） | 同上 | 无文件时 1 | 否（只 POST 上游） |

约定：

- 只有 `cmd/server` 用 `flag`；其余手写位置参数解析（`cmd/login` 自解析 `--realm`，未知子命令返回 `unknown subcommand`）。
- 人读表格与机器可读 JSON 分两路：状态词固定（`OK` / `ALREADY` / `FAIL` / `AUTH_INVALID` / `LOAD_ERR` / `N/A`），便于 grep 与脚本解析。
- `cmd/login` 为了保持二进制独立，**故意复制**了服务端的 envelope/HTTP 逻辑（注释写"与 oauth.go 一致"）。改协议时两处都要看，但不要强行抽公共包——它是独立发布的 CLI。
- 共享 `internal/auth` / `internal/upstream`：凭证解析、刷新、realm 解析、业务调用都复用，不在 CLI 里重写。

**排查纪律**：`cmd/signin` 会改写 `auths/*.json`，`login.sh` 还会 `docker restart` 正在服务的容器。只读排查不要运行它们，用 `cmd/credit` 或直接读文件。

## Python 脚本（`scripts/`）

`task_common.py` 是共享库，其它脚本 import 它：

- 常量：`CHAT_BASE` / `BILL_BASE`、各 API 路径、`CLIENT_UA`。
- 凭证：`load_auth(uid_or_file)`，文件名约定 `workbuddy-<uid>.json`；传 uid **前缀**时按 `workbuddy-{pre}*.json` glob 取第一个。所有文件读写显式 `encoding="utf-8"`（Windows 默认 GBK 会炸）。
- HTTP：`do_get` / `do_post` 返回 `(status, dict)`；业务封装 `list_tasks` / `accept_tasks` / `claim_reward` / `chat_event` / `report_activity` / `chat_completion`。
- **默认 dry-run**：写操作必须显式 `--yes`。新脚本沿用这个开关语义。
- `probe_max_tokens.py` 与账号无关：探 OpenAI 兼容网关的 `max_tokens` 上限，支持 `--resume` 与 `--panel-out` 契约文件。

已知缺陷（照现状记录，别当成范例）：

- `task_common.AUTHS` 是硬编码的 `/root/workbuddy2api/auths`，没有环境变量覆盖；镜像里的路径是 `/app/auths`。容器内直接跑这些脚本会失败。
- `probe_active.py` 有一处 `json.load(open(...))` 漏了 `encoding=`。

## Shell 脚本

| 脚本 | 作用 | 关键行为 |
|------|------|----------|
| `login.sh` | 完整登录 → 落盘 → 重启容器 | 无 `./login` 时先构建 → `login url` → 复制到剪贴板 → 交互确认 → `login poll` → python3 逐字段解析 → **通过环境变量**把值传给签到 heredoc（避免注入）→ 写 `auths/workbuddy-<uid>.json` → `docker restart` + `/status` 校验 |
| `signin.sh` | 批量签到 | 无 `./signin_bin` 时先构建，再 exec |
| `credit.sh` | 余额 | **不构建**，直接 exec `./credit`（需预构建） |
| `scripts/docker.sh` | 本地镜像/容器 | 子命令 `env\|build\|up\|run\|push\|down\|stop\|restart\|logs\|ps\|pull\|compose\|help`；默认值与 `docker-compose.yml` 必须一致（镜像 `misaka10092/workbuddy2api-panel`、tag `latest`、端口 7863、容器名 `workbuddy2api`）；`build`/`up` 自动算好版本要素（tag / 提交 / 构建时刻）当 build-arg 传进去 |

两条路径别混淆：**面板"添加账号"是热加载进池（免重启）**；`login.sh` 走的是**重启容器**加载。

## 镜像（`Dockerfile` / `docker-compose.yml`）

- 两阶段：`golang:1.23-alpine` 构建 → `alpine:3.20` 运行。
- 四个二进制在一次 `RUN` 里编出来，统一 `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`；`wb2api` 另外注入版本要素（build-arg `BUILD_VERSION` / `BUILD_COMMIT` / `BUILD_TIME`）。镜像里没有 `.git`（`.dockerignore` 排除 + 构建镜像不装 git），hash 只能从外面传；`BUILD_TIME` 缺省取构建时刻。
- 运行阶段装 `wget ca-certificates tzdata python3 bash`，创建 uid 10001 的 `app` 用户。
- 脚本拷进去后用 `sed -i 's/\r$//'` 去 CRLF（Windows 检出），**必须在对 `app` 切换用户之前以 root 执行**——`app` 对 root 拥有的文件无写权限。
- 镜像不带真实配置：`config.example.json` 作为 `/app/config.json` 的默认；生产靠挂载覆盖。
- `HEALTHCHECK` 是 `wget /healthz`；`ENTRYPOINT ["/app/wb2api","-config","/app/config.json"]`。
- 只把 `scripts/probe_active.py` 放进镜像（其它 Python 脚本是开发机工具）。

## 发布

- 版本串来源、tag 规则、`-s -w` 口径见 [quality-and-testing.md](./quality-and-testing.md)。
- `.github/workflows/docker-ghcr.yml`：多架构（amd64/arm64）推 GHCR，`latest` + semver + sha tag，`provenance: false`（避免 ghcr 出现 unknown 架构条目），PR 只构建不推；按 ref 类型传版本要素（tag 构建传 tag，其余只传提交号 + 构建时刻）。
- `.github/workflows/go-binaries.yml`：`test` → 五平台交叉编译（注入版本要素）→ 断言产物含 tag（tag 构建）/ 本次提交号（所有构建）→ 打包（Windows 三件套 zip，其余 tar.gz）→ tag 触发时生成 checksums 并发 Release。
- 本地产物 `wb2api.exe` 及其编辑器备份 `wb2api.exe~`、`auths.zip` 在仓库根目录是**运行产物，不是源码**，不要提交也不要当参考实现。

## 反模式

- 在 CLI 里复制服务端的池/调度逻辑（应调用 `internal/*`）。
- 新增脚本却不提供 dry-run / `--yes` 开关。
- 让脚本默认写 `auths/`。
- 修改 `scripts/docker.sh` 默认值却不同步 `docker-compose.yml`。
- 在 Dockerfile 里把 `sed -i` 挪到 `USER app` 之后。
