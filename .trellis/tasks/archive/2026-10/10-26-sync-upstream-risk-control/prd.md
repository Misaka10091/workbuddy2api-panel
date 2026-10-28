# 同步上游风控改动

## Goal

同步上游 `misakano7545/workbuddy2api-panel`（HEAD `37b3f4c`）的风控相关改动：出站指纹、
请求频控、状态正确性、观测与暴露面，含回归测试。本地是手工挑选同步分支（分叉点
`dbd7c68`），不能直接 merge，需逐项按上游语义移植并适配本地结构。

## Requirements

### P0 出站指纹（上游 issue #51，直接关联禁号）

- `/v3/config` 探测 UA 由 `CodeBuddyIDE/...` 改为官方 `WorkBuddy/<ver> ... CLI/<cli>` 两/三段式
  （CN 三段、global 两段），第二路用单品牌两段式。
- chat 头族剥离 `Origin`/`Referer`/`Accept-Language`/`X-Machine-ID`/`X-Session-ID`；
  不再发 `X-No-Enterprise-Id`/`X-No-Department-Info`；`Accept` 只 `application/json`；
  恒发 `X-Private-Data: true`；`X-Product` 恒 `SaaS`（产品名走 `X-IDE-*`）。
- 桌面任务链（`DesktopChatWithExpert`）不再手搓头，统一走 `ChatHeaders`。
- 桌面上报指纹补 `cpuModel`、CN 的 `qimei36`，设备值改真机口径；
  `X-Domain` 发裸主机名；`X-Request-ID` 用 32 hex 消息 ID；会话 ID 用 v4 UUID。
- 清除所有出站 `wb2api-*` 自证前缀（autotask / scheduler / travel / blackcat / school / desktop）。

### P1 请求频控

- 账号遍历任务（签到 / 旅行 / 活跃 / 保活 / 夜猫子）按窗口摊开：`hash(任务|uid|日)` 定偏移，
  冲突按最小间隔推开，遍历按计划时刻排序；手动入口不摊开。
- 余额刷新：ticker 加稳定抖动，账号级起跑错峰。
- 夜猫子单轮参与数上限，按日轮换参与账号。
- 面板五个全量接口共用一个防重入闸门（409），`/panel/api/models` 60s 成功缓存，
  `refresh=1` 绕过。
- 脱敏补 `DeepSeek Harness → DeepSeekRuntime` 品牌词与 `sanitize_brand_scope`（all/identity）。

### P2 状态正确性

- 粘性 `touch` 改 CAS：写锁内核对绑定仍是入参 uid，否则放弃本次 touch。
- `NoteSessionDead` 阈值内 +1 与 `ClearSessionDead` 清零都置 dirty（重启不丢进度/不复活计数）。

### P3 观测与暴露面

- 启动弱密钥告警（空 key / `test_key` + 非回环监听）。
- reqlog 归档新增 `tools_declared` / `tool_calls`（流式按 index 去重 + 还原数，非流式两种组包形态都认）。
- `models.allowed` 网关级白名单：列表与 chat 入口取交集，条目支持 `cn:`/`global:` 前缀。
- 积分闸：当日上限 / 5 分钟突发 / 连续低缓存率三道，只认已观测 `usage.credit`。

## Acceptance Criteria

- [x] `go build ./...`、`go vet ./...`、`go test ./...` 全绿。
- [x] 新增回归测试覆盖：出站头族、桌面指纹、touch CAS、12153 脏标记、品牌词作用域、
      白名单规则、预算三道闸、摊开确定性/最小间隔、夜猫子上限、面板闸门与缓存、弱密钥矩阵。
- [x] 既有契约零漂移：错误码分类、6004 模型级冷却、首帧否决、`X-Conversation` 三件套。

## Notes

- 本地无静态 API Key 账号形态，上游的 `X-API-Key` 双头发送项不适用，未移植。
- 本地无图片/搜索路由，白名单的四点执法收敛为「`/v1/models` + chat 入口」两点。
- CN 对话主域改 `www.workbuddy.cn`（上游 `741cf59`）涉及路由变更，单列待决策，未纳入本次。
