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
