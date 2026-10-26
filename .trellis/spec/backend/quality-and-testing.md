---
name: backend-quality-and-testing
description: 验证命令、测试写法与确定性技巧、回归测试形状、版本与发布纪律
paths:
  - "**/*_test.go"
  - go.mod
  - .github/workflows/**
---

# 质量与测试

## 验证命令（提交前）

```bash
go build ./...
go vet ./...
go test ./...
```

- CI 只跑 `go test ./...`（`.github/workflows/go-binaries.yml`），构建另外做五平台交叉编译。
- **没有** Makefile、`.golangci.yml`、`staticcheck`、`gofmt -l` 门禁。`go vet` 就是全部静态检查。别引入新的 lint 工具链。
- `go` 在本机可能不在 PATH：已确认位于 `C:\tools\go\bin\go.exe`。
- `internal/panel` 的 JS 测试用 `exec.LookPath("node")` 做前置；缺 node 时 `t.Skip`。CI 的 ubuntu runner 自带 node，所以**本机绿 ≠ JS 已验证**（要真跑需本机有 node）。
- 测试不依赖网络：所有上游交互都是 `httptest` 或注入的 `RoundTripper`。

## 测试写法

全 stdlib：只用 `testing`，没有 testify/assert。断言是 `t.Errorf` / `t.Fatalf`。

### 文件与包

- 与被测文件同目录、`<name>_test.go`、**同包名**（`package server` / `package pool`），以便测未导出符号。
- 共享夹具放在该包某个 `_test.go` 顶部（如 `handler_test.go` 的 `sseOK`）。
- 真实抓包样本放 `testdata/`（例：`internal/upstream/testdata/leaked_markup.txt`）。

### 两种常见形状

1. **直叙式**：单场景一个 `TestXxx`，body 里 `t.TempDir()` → 写文件 → 调用 → 断言。
2. **表驱动**：匿名 struct 切片 + `t.Run`，或用 `map[string]string` 表达"缺省/空/ null/空数组"这类维度。用例名可以是中文。

### 复用既有构造函数

| 需要 | 用 |
|------|----|
| 面板 handler | `newTestPanel()` = `New(Config{Version:"test", APIKey:"test-key"})`（带鉴权，避免 nil 依赖） |
| 调度器 | `newTestScheduler()` / `newTravelScheduler(t, srv, uids...)` |
| 假上游（断言 wire 内容） | `newTestUpstream(t, handler)` + `httptest.NewServer` |
| 假上游（只关心解析） | `rtFunc` + 最小 `Client` 字面量 + `testClient(...)` |
| 可确定选号 | `p.SetRandomSource(func(int64) int64 { return 0 })` + `testPoolWith(auths...)` |

### 确定性（本仓库的防 flake 手册）

- **不用假时钟**。冷却相关测试改**数据**而不改时钟：`expireCooldown` 直接把 `e.until` 拨到过去。
- 时间断言用**容差带**（`wantCoolSec(t, p, uid, want, tol)`），不要断言精确时刻。
- 真正时间驱动的行为（自动 flush、TTL）用**有上界的轮询**，不用裸 `time.Sleep`；改 `flushInterval` 这类包级变量后必须 `t.Cleanup` 还原。
- 需要"等一小会儿"的地方必须写清理由（例：Windows 上 TTFB 要跨过时钟粒度）。
- `t.Parallel()` 只在"完全不碰包级全局状态"的纯函数测试里用（现状仅 `internal/auth` 有 5 处，且每处注释写明"不触碰全局开关"）。`internal/server`、`internal/pool`、`internal/scheduler` 全包没有并行：它们要改 `chatLogEnabled`、`wafIPWindow`、`rotateBackoffBase`、`flushInterval`、`activityAccountDelay` 这类包级变量，改动后必须 `t.Cleanup` 还原，别加并行。
- 时间逻辑尽量抽成纯函数用注入的 `time.Time` 测（`nextMidnightCST`、`tokensPerSecond`、`nextFire`、`nextWake`）。
- 破坏性文件系统场景用"父路径是普通文件"制造稳定失败（比 chmod 可靠）。

## 回归测试形状

本仓库要求：**把 bug 恢复回去，测试必须失败**。新增修复应同时给出这条形状的测试（`frontend_test.go` 里对过期提示框/渲染路径都有明确注释）。

配套习惯：

- 每个非平凡测试带一句中文 doc comment，写清"这条断言守护的语义"和"它来自哪个 issue/事故"（仓库里到处是 `issue #27`、`#100`、`#102`、`#114`、`#136`、`go test -race 实证` 这类引用）。
- 配置默认值必须**在 `normalize()` 之后**断言（断的是生效值，不是裸结构体字段）；时长用 `Seconds()/Minutes()/Hours()` 比。
- 每加一个特性，同时补三类断言：显式配置生效、缺省回落、**老配置行为不变**。

## 会随代码一起改的"冻结断言"

这些断言是有意的回归护栏，改协议/格式前必须同步更新，不能直接删：

- SSE：精确帧数、`[DONE]` 恰好一个、`normalizeFrame` 逐字节 JSON、`finish_reason` 必须排在合成 tool_calls 帧之后。
- 流水行：列格式与固定宽度。
- 面板：`CFG_MAP` ↔ `name="…"` 双向一致、各视图的结构标记字符串。
- 配置：`TestHardCreditKeyIgnored` 这类"未知/退役键被忽略"的断言。

## 版本与发布纪律

- 版本唯一来源：`cmd/server/main.go` 的 `const appVersion = "1.13.0-panel"`。没有 ldflags 注入。
- CI 会 `grep` 这个常量、断言产物二进制里含该字符串，并在打 tag 时断言 `tag == appVersion`（先剥掉 `-panel` 后缀）。`*-ci` 结尾的 tag 跳过一致性断言（演练发布线用）。
- 构建参数固定 `-trimpath -ldflags="-s -w"`，`CGO_ENABLED=0`，产物名 `wb2api`（Windows 带 `.exe`）；发布资产名 `wb2api-panel-v<ver>-<os>-<arch>.zip|tar.gz`。
- 仓库里同时存在两族 tag：semver `v1.x.y` 与时间戳 `v2026.9.30.2027`。**只有 semver 族能过发布断言**，别拿时间戳 tag 打 release。tag 命名与打 tag 的完整流程见 [../collaboration/git-workflow.md](../collaboration/git-workflow.md)。
- `go.mod` 声明 `go 1.22.5`（CI 按它装），Dockerfile 用 `golang:1.23-alpine` 构建 —— 两处版本刻意不同，不要"顺手对齐"。

## 注释风格

- 不用 `TODO` / `FIXME` / `HACK` 标记（全仓库近乎为零）。技术债写成散文，**引用 issue 号或事故现象**。
- 注释写"为什么"，特别是"为什么不能反过来做"。反向修改者最需要的是这条信息。
- 已知的注释漂移：`internal/pool` 少数 doc comment 因重构跑到了相邻函数上方。判断行为以函数体为准，顺手修正时不要改变语义。

## 反模式

- 引入 testify / gomock / golden 断言库。
- 为通过测试改断言容差或删断言。
- 用 `time.Sleep` 替代注入 RNG / 回拨截止时间。
- 在 `internal/server` 加 `t.Parallel()` 却不隔离包级变量。
- 测试里访问真实上游或真实 `auths/`（会污染真实账号）。
- 用时间戳 tag 发布。
