---
name: collaboration-index
description: 协作规范入口：分支、commit、署名、PR、tag 与"什么不该提交"
---

# 协作规范入口

本目录管"代码怎么写"之外的另一半：**改动怎么进版本库**。多人 + AI 共同提交的仓库里，这一半和代码质量同等重要——本仓库历史上出现过误提交风险（`auths/` 明文 token）、署名格式分叉、以及文档因 `*.md` 被忽略而没能进库。

## 文件地图

| 文件 | 管什么 |
|------|--------|
| [git-workflow.md](./git-workflow.md) | 分支模型与命名、commit message 格式（type/scope/body）、作者与 AI 署名、PR 流程、tag 与发布、忽略与行尾、提交前检查表 |

## 硬规则（记不住细节也要记住这些）

1. `main` 只接 PR 的 **merge commit**；产品代码禁止直推。
2. 分支名 `<type>/<kebab-主题>`，type 与 commit type 同一套；合并后删分支。
3. commit 标题 `<type>(<scope>): <中文摘要>`；body 必须写**根因、取舍、测试、验证**。
4. `author` 只能是真实人类 + GitHub noreply 邮箱；AI 用 `Co-authored-by:` 尾注（统一小写 `by`）。
5. tag 就是发布版本串（`v1.14.0` / `v2026.10.27.2038` 都合法）；源码里没有版本常量，别再找 `appVersion`。
6. 禁止 `git add -A`：`*.md` 全局被忽略、`auths/` 是明文凭证、根目录有运行产物。

## 提交前检查表

```bash
git status --porcelain
git diff --staged
git add <显式路径>
go build ./... && go vet ./... && go test ./...
git commit      # <type>(<scope>): 摘要  + 根因/改动/测试/验证
```

Trellis 侧的收尾流程（归档任务、写 journal、`add_session.py` 自动提交）见 [../../workflow.md](../../workflow.md)。
