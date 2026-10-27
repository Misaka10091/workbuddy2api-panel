---
name: collaboration-git-workflow
description: 分支管理、commit message、AI/人类署名、PR 流程、tag 与忽略规则
paths:
  - .gitignore
  - .gitattributes
  - .github/workflows/**
  - AGENTS.md
---

# Git 与协作规范

本仓库是**多人 + AI 共同提交**的仓库（历史上 6 位以上人类作者，AI 协作已有 `Co-authored-by` 先例）。
下面每一条都对应真实历史或真实事故；与现状不一致的地方会标注「现状」与「应做」。

## 1. 分支模型

采用 trunk-based：`main` 始终可发布，一切改动走短生命分支 + PR。

| 规则 | 说明 |
|------|------|
| 禁止直接推 `main` | 唯一例外：Trellis 的 bookkeeping 提交（`.trellis/tasks` 归档、journal）可以直推，但**不含产品代码** |
| 分支名 `<type>/<kebab-主题>` | type 与 commit type 同一套：`feat/` `fix/` `chore/` `docs/` `refactor/` `ci/` `sync/`。例：`feat/upstream-api-call-layer`、`fix/insecure-context-copy` |
| 工具生成的 `codex/…` 分支可保留 | 历史存在 `codex/fix-prompt-cache-key-session-fallback`，不必改名，但 PR 标题仍按 commit 规范 |
| 分支存活 ≤ 几天 | 长期分支必然冲突；大改动拆成多个 PR |
| 合并后删除分支 | 「现状」本地仍有已合并的 `feat/upstream-api-call-layer`、`fix/insecure-context-copy` 残留，应清理 |
| 同步上游用独立分支 | `git fetch upstream` → `sync/upstream-<主题>` → 提交类型 `sync(upstream):` |

上游与本地远端：

```bash
origin    git@github.com:Misaka10091/workbuddy2api-panel.git   # 本仓库
upstream  https://github.com/misakano7545/workbuddy2api-panel.git
```

改基线与同步：

```bash
git switch main && git pull --ff-only
git switch -c fix/<主题>
git rebase main            # 私有分支保持线性，便于审阅
git push -u origin HEAD
```

- force-push 只允许发生在**自己的**未合并分支上，且必须 `--force-with-lease`（裸 `--force` 会覆盖别人的提交）。
- **绝不** force-push `main`。

## 2. Commit message

### 标题格式

```
<type>(<scope>): <中文摘要>
<type>(<scope>)!: <中文摘要>      # 破坏性变更（本仓库暂未出现过，慎用）
```

- 摘要用中文，一句话说清"做了什么 + 为什么值得提交"，不加结尾句号，约 50 字以内。
- 括号里是模块名；无明确模块时省略括号。

**type**（全部来自真实历史）：

| type | 用途 | 历史示例 |
|------|------|----------|
| `feat` | 新功能 | `feat(panel): 账号表多选 + 批量操作` |
| `fix` | 修 bug | `fix(pool): 修 CooldownModelBusy 阶梯测试的 flaky（CI 上必挂）` |
| `sync` | 对齐上游实现（本仓库特有） | `sync(upstream): 出站 UA 族按双版本真机抓包对齐` |
| `chore` | 维护性改动 | `chore: record journal` |
| `docs` | 文档 | `docs(spec): …` |
| `test` | 只加/改测试 | `test(upstream): 新 kind 的 gateway_hint + 出站报文端到端护栏` |
| `refactor` | 不改行为 | `refactor(panel): …` |
| `ci` | 工作流 | `ci: …` |

**scope** 常用值：`panel` `server` `upstream` `pool` `scheduler` `usage` `tasks` `models` `global` `realm` `apikeys` `prompt` `spec` `ci`。

### Body（本仓库已成型的写法）

```
<标题>

<为什么：现象 → 根因 → 证据（issue 号 / 上游提交 / -race 输出 / 抓包）>

改动
- <要点一>：<取舍与理由>
- <要点二>

测试
- <用例文件>：<覆盖了什么、钉住了哪条语义>

验证：go build ./... 通过；go test ./... 全绿。

Co-authored-by: <Agent> <noreply@…>
```

必须包含的信息：

- **根因**，不是"改了什么"。历史提交普遍写清"此前为什么这样是错的"（例：6004 写入 `until` 把单模型限流升级成整号冷却）。
- **取舍**：为什么选 A 不选 B（例：批量操作只对可见账号生效、部分失败不回滚）。
- **验证证据**：跑过的命令与结果，测试数量与结论。
- 关联：`Refs: #123` / `Closes: #123`。

一次提交只做一件事。格式化、重排、版本号这类噪音改动单独一个提交，不和功能混在一起。

## 3. 署名（作者、AI 协作、签名）

### 3.1 author 必须是真实人类

- commit 的 `author` 只能是实际负责任的人类账号；**AI 不作 author**。
- 用 GitHub noreply 邮箱，避免真实邮箱泄漏到公开历史：

```bash
git config --global user.name "Misaka10091"
git config --global user.email "27477663+Misaka10091@users.noreply.github.com"
```

「现状」历史里有 `851830155@qq.com`、`alexinby719@139.com` 等真实邮箱；新提交不要沿用。
「应做」仓库级覆盖只在确实需要时用 `git config --local`，不要拿别人的身份提交。

### 3.2 AI 协作的署名

AI 实质参与代码或文档产出时，在 body 末尾空一行加协作者尾注：

```
<Agent 名称> <该代理官方提供的邮箱>
```

仓库里真实出现过的两条（作为命名格式参考）：

```
Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>
Co-Authored-By: Claude Code <noreply@anthropic.com>
```

- 规范写法统一为 **`Co-authored-by:`**（小写 `by`）。「现状」历史里同时存在 `Co-authored-by:`（Copilot）与 `Co-Authored-By:`（Claude Code），新提交统一前者，别再造第三种。
- **只署名真正参与了产出的代理**；只做审阅、答疑、跑命令看结果的不加。
- 不虚构协作者：不要写不存在的用户名/邮箱，也不要给人类同事加 `Co-authored-by` 代替 `author`。
- GitHub 会把 `Co-authored-by` 解析成共同头像与贡献统计，所以这一行是**公开可见的署名声明**，加之前确认内容准确。
- 使用 GitHub Web 端 squash/merge 时尾注会保留，但多作者信息可能被折叠——重要协作仍建议本地 merge commit。

### 3.3 DCO 与签名（GPG/SSH）

- 本仓库**未采用 DCO**，历史零 `Signed-off-by`，因此不要擅自加。若将来启用：`git commit -s` 生成 `Signed-off-by: 姓名 <邮箱>`。
- 提交签名（GPG/SSH）是**可选**增强，CI 不校验。若某位维护者开启：

```bash
git config --global commit.gpgsign true
git config --global user.signingkey <key-id|ssh-key-path>
```

  注意 rebase 与 merge commit 的签名行为（rebase 会重写签名），以及 CI 里 `/releases` 的资产校验不依赖签名。

## 4. PR 流程

- **标题 = 主 commit 标题**（例：`sync(upstream): 对齐上游 API 调用层（错误码/报文/刷新/流语义）`）。
- 描述用与 commit body 相同的四段：为什么 / 改动 / 测试 / 验证，外加：
  - 影响面：协议（OpenAI 响应/上游报文）、面板接口、配置字段（含是否**重启生效**）、发布行为。
  - 回滚方式：能否单纯 revert；涉及状态文件/配置 schema 的改动必须写清回滚影响。
- 关联 issue：`Closes #N`（合并后自动关闭）。
- CI（PR 必跑）：
  - `.github/workflows/go-binaries.yml`：`go test ./...` + 五平台交叉编译 + 二进制版本串断言。
  - `.github/workflows/docker-ghcr.yml`：仅当改动命中 `Dockerfile` / `go.mod` / `cmd/**` / `internal/**` / `scripts/**` / `*.sh` / `config.example.json` 时构建验证（不推送）。
- 合并方式：**merge commit**（保留 PR 号，历史一致，52 个 merge commit 都是这个形态）。不做 squash——squash 会丢掉 PR 内的分步提交，而本仓库的提交粒度本身就是审阅材料。
- 合并后：删除远端分支；若 PR 含重启生效字段，在发布说明里点名。

## 5. Tag 与发布

- tag 本身就是面板版本串：打 tag 构建时 CI 把 tag 名 `-X` 进二进制，左上角原样显示（`v1.14.0` / `v2026.10.27.2038`）。源码里**没有**版本常量，所以不存在"先改版本号再打 tag"这一步。
- 没有 tag 的构建显示 `v<构建时间 +08:00>-<提交短 hash>`（`v2026.10.27.2040-1a2b3c4`），本地 `go build` 与 Docker 构建都如此。
- 仓库里有两族 tag：semver `v1.x.y`（最新 `v1.13.0`）与时间戳 `v2026.9.30.2027`。两族都能发——版本串直接取 tag，CI 只校验 tag 形态（`v` + 数字开头）并在产物里断言该串。
- `-ci` 后缀的演练 tag 不再是特例（没有一致性断言可跳过），但它会生成一个带 `-ci` 的发布名，正式资产仍用干净 tag。
- 打 tag 前：`git switch main && git pull --ff-only` → 确认要发布的提交已在 `main` → `git tag vX.Y.Z` 并推送（`git push origin vX.Y.Z`）。
- 发布细节（`-s -w`、五平台资产名、checksums）见 [../backend/cli-and-scripts.md](../backend/cli-and-scripts.md) 与 [../backend/quality-and-testing.md](../backend/quality-and-testing.md)。

## 6. 什么不该提交（本仓库特有）

当前 `.gitignore` 的相关规则（`git check-ignore -v <path>` 可复核）：

| 规则 | 效果 |
|------|------|
| `auths/`、`data/`、`config.json`、`*.key`、`*.pem`、`.env*`、`backups/` | 凭证与运行状态，永不入库 |
| `*.md` + `!README.md` + `!.trellis/**/*.md` | **`.trellis` 下的 markdown（spec / tasks / workflow / agents）可入库**；`.trellis` 之外的 `*.md` 仍被忽略 |
| `*.zip`、`/wb2api.exe`、`/login.exe`、`/credit.exe`、`/login`…（根目录锚定） | 二进制与发布包 |
| `*~`、`/.diffdump/`、`/.diffscratch/` | 编辑器备份与本地临时目录 |
| `docs/`、`LOOP.md`、`PLAN.md`、`SPEC.md`、`/cmd/desktopverify*/`、`server.exe` | 本地设计文档与验证工具 |

仍然存在的两个陷阱：

1. **`.trellis` 之外的 markdown 进不了库**：根目录设计文档、`.github/pull_request_template.md`、`docs/**` 都在忽略范围内。要么加一条 `!` 规则，要么 `git add -f <path>`（`internal/prompt/defaultprompt.md` 就是被强制加入的）。
2. **只覆盖了 `~` 结尾的备份**：`wb2api.exe.bak`、`*.orig`、`*.rej` 之类没有规则。`git add -A` 会把它们一起带进去。

因此：

- **禁止 `git add -A` / `git add .` / `git commit -a`**。一律显式列路径：`git add internal/pool/cooldown.go internal/pool/modelcooldown_test.go`。理由是规则只能挡住"想到过的"路径，而 `auths/` 里是**明文** access/refresh token——漏一条就是交出账号。
- 提交前 `git status --porcelain` 逐条过，再用 `git check-ignore -v <路径>` 确认"以为被忽略"的文件真的被忽略。
- 新增一类本地产物时，先补 `.gitignore` 规则再动手，不要依赖 `git add` 时的自觉。

行尾：`.gitattributes` 强制 `*.sh` / `Dockerfile` / `*.yml` / `*.yaml` 用 LF——CRLF 会让容器里 shebang 变成 `/bin/bash^M` 而直接启动失败。Windows 上编辑这些文件后要确认 `git diff` 没有整文件行尾翻转。

## 7. 提交前一分钟检查表

```bash
git status --porcelain          # 只应有你打算提交的路径
git diff --staged               # 逐行看，找夹带
git add <显式路径>              # 不要 -A
go build ./... && go vet ./... && go test ./...
git commit                      # 标题按 <type>(<scope>): 摘要；body 写根因/改动/测试/验证
git log -1 --format='%B'        # 自检：摘要、正文、尾注是否齐全
```

AI 参与时最后再加一行 `Co-authored-by: <Agent> <noreply@…>`。

## 反模式

- 直接往 `main` 推产品代码；或 force-push `main`。
- 一次提交里混进"顺手格式化"（review 时会把真实改动淹掉）。
- 摘要写成"更新代码""修复问题"这种无法归类的描述。
- 用 `Co-Authored-By` / `Co-authored-By` / `co-authored-by` 等变体制造第三种写法。
- 给只做了 review 的代理挂 `Co-authored-by`，或虚构协作者。
- 用真实私人邮箱提交（尤其公开仓库）。
- 指望"改了源码里的版本号就发新版本"：版本串只来自 tag（或非 tag 构建的构建时间 + 提交 hash），源码里没有版本常量可改。
- `git add -A` 把 `auths/`、`wb2api.exe~`、`.diffdump/` 带进版本库。
- 合并后不删分支，或长期分支不 rebase 导致 PR 全是冲突噪音。
