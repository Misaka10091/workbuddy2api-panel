# Bootstrap：把 `.trellis/spec/` 写成这个仓库的真实规范

## 目标

用一次性 spec bootstrap，把 `.trellis/spec/` 从 Trellis 模板占位符替换为**本项目当前代码的真实约定**，使后续 `trellis-implement` / `trellis-check` 子代理加载后写出的代码贴合本仓库风格，而不是通用脚手架。

## 范围

- 写入目录：`.trellis/spec/`
- 分析来源（只读）：
  - `cmd/server`（启动 + 配置 + 面板装配）
  - `cmd/login` / `cmd/credit` / `cmd/signin` / `cmd/trial`
  - `internal/server`（OpenAI 兼容 HTTP 层）
  - `internal/upstream`（上游协议层）
  - `internal/pool` / `internal/scheduler` / `internal/session` / `internal/redisstore`
  - `internal/panel`（Go 面板 + `index.html` + `app.js` 内嵌前端）
  - `internal/apikeys` / `internal/usage` / `internal/reqlog` / `internal/logfmt` / `internal/prompt`
  - `internal/auth` / `internal/httpauth` / `internal/livecfg`
  - `scripts/`、根目录 `*.sh`、`Dockerfile`、`.github/workflows/`
  - 各包 `*_test.go` 与 `README.md`
- 不在范围：产品代码改动、行为变更、依赖升级。

## 架构结论（分析后确定）

单个 Go module（`github.com/linguo2625469/workbuddy2api-panel`，`go 1.22.5`），无 monorepo 子包，因此不按 package 拆 spec 目录，而按**真实职责边界**分层。

1. `cmd/server` 负责装配（配置 → auth → pool → upstream → scheduler → livecfg → panel → handler）。
2. `internal/server` 是 OpenAI 兼容入口，只做鉴权、轮换、改写编排与响应整形。
3. `internal/upstream` 才拥有上游协议细节（payload 改写、SSE 白名单、DSML 修复、出站头族、错误分类）。
4. `internal/pool` 是账号状态机与唯一惩罚落点；`internal/scheduler` 只按排程调用它。
5. `internal/panel` 的 Go 代码只做 HTTP 编排；HTML/CSS 在 `internal/panel/index.html`，全部 JS 在 `internal/panel/app.js`（`go:embed`），因此前端自成一层规范。

## 产出文件

- `.trellis/spec/backend/`：`index.md`、`directory-structure.md`、`config-and-hot-reload.md`、`http-api-layer.md`、`error-handling.md`、`upstream-protocol.md`、`pool-lifecycle.md`、`logging-and-observability.md`、`quality-and-testing.md`、`cli-and-scripts.md`
- `.trellis/spec/frontend/`：`index.md`、`panel-assets-and-security.md`、`panel-ui.md`、`testing.md`
- `.trellis/spec/guides/`：重写 `index.md`、`cross-layer-thinking-guide.md`、`code-reuse-thinking-guide.md` 为项目实证版本
- `.trellis/spec/collaboration/`：`index.md`、`git-workflow.md`（分支模型、commit message、作者与 AI 署名、PR、tag、忽略规则）
- 删除不适用的模板文件（`database-guidelines.md`：本项目无 ORM/迁移，持久化写入 `pool-lifecycle.md`）
- `.gitignore`：补 `!.trellis/**/*.md` 例外（否则 `*.md` 全局忽略会让 spec 进不了版本库），并补 `*~`、`/.diffdump/`、`/.diffscratch/`

## 约束

- 每条规范必须有仓库内证据（文件路径 / 符号 / 测试）。
- 不写理想化建议，不写本仓库不存在的工具链（例如 golangci-lint、testify）。
- 不得留下占位符、空标题或复制来的模板套话。
- 引用行号为**参考锚点**（可能随后续改动漂移），以符号名 + 文件路径为准。
- 文档语言与仓库一致：中文（代码标识符、路径、配置键保持原样）。

## 验收标准

- [ ] `.trellis/spec/` 中不存在占位符（`To fill` / `TODO: fill` / `placeholder` / 空标题）。
- [ ] 各 `index.md` 与实际文件集合一致，无失效链接。
- [ ] 每个主题文件都带 `paths:` frontmatter，且 glob 合法、能覆盖其管辖源码。
- [ ] 关键规则均指向真实文件/符号/测试，无凭空发明的约定。
- [ ] 明确记录本项目独有的纪律：`livecfg` 原子快照、重启生效字段清单、错误分类顺序、轮换/惩罚边界、SSE 帧白名单、DSML 标记还原、内嵌前端测试标记。
- [ ] 记录已知文档漂移（README 与代码不一致处），避免后续代理照抄 README。
- [ ] 协作规范落库：分支/commit/署名/PR/tag 规则可执行，且 `.trellis/spec` 的 markdown 能被 git 正常追踪。
