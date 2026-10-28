# 同步 CN 对话主域至 www.workbuddy.cn

## Goal

对齐上游 `741cf59`：国内版 chat / 模型目录 / growth 域（旅行、连登）的出站主入口由
`https://copilot.tencent.com` 改为官方桌面端 `product.json` 的 endpoint
`https://www.workbuddy.cn`，并保留 `cn.chat_base` 回填备选入口的能力。

## Requirements

- `internal/upstream.New()` 的 `ChatBaseCN` 缺省改为 `https://www.workbuddy.cn`。
- 新增配置 `cn.chat_base`：空 = 用内置默认；非空 = 覆盖 `ChatBaseCN`（供海外出口
  遇到 `copilot.tencent.com` TLS 复位时回填）。上游该键已存在，本地缺失需补齐。
- 保持 `BillingBaseCN = https://www.codebuddy.cn` 不变：CLI 指纹类任务计分依赖该域。
- 保持 OAuth 登录域 `copilot.tencent.com` 不变（`panel/login.go`、`cmd/login`）。
- 更新 README 的 base 说明、任务计分说明与架构图；更新 travel/desktop 注释。
- `cn.chat_base` 属装配期字段，写入 `restartRequiredFields`。

## Acceptance Criteria

- [ ] `Client.chatBase(&auth.Auth{})` 返回 `https://www.workbuddy.cn`；global 账号仍走 global base。
- [ ] `/v3/config`、chat、travel 三类路径全部落到新域（复用同一 `chatBase` 出口）。
- [ ] `cn.chat_base` 非空时覆盖生效；空时不覆盖。
- [ ] billing 域与登录域断言不变。
- [ ] `go build ./...`、`go vet ./...`、`go test ./...` 全绿。

## Notes

- 不涉及 global 域、web 域与 `X-Domain` 的账号落盘 domain 优先逻辑。
- 配置只增不改：旧配置（无 `cn` 节）行为等于「使用新缺省域」。
