---
name: backend-upstream-protocol
description: 上游协议层：payload 改写顺序、SSE 帧白名单、DSML 工具标记还原、工具配对、出站身份与脱敏、端点清单
paths:
  - internal/upstream/**
---

# 上游协议层

`internal/upstream` 是唯一知道"CodeBuddy 上游长什么样"的地方。它的输出必须让上游认，输出给客户端的部分必须让 OpenAI 客户端认。

## 请求体改写：顺序即契约

入口 `Client.prepareBody` → `PrepareBodyOptWithEffortsDefaultAndCaps`（`payload.go`）。步骤顺序不可调换（下面每步都写"为什么在这个位置"）：

| # | 步骤 | 函数 | 不可调换的理由 |
|---|------|------|----------------|
| 1 | 剥离 `cn:` / `global:` 前缀 | `normalizeModelName` | 后续所有查表都用**裸模型名**，晚一步就查空 |
| 2 | `stream=true` | 内联 | 上游不接受非流式 |
| 3 | `max_completion_tokens` → `max_tokens` | `translateMaxCompletionTokens` | 别名无论是否翻译都删除；显式 `max_tokens` 优先 |
| 4 | `gpt-*` 下限 16 | `clampGPTMinMaxTokens` | 小于 16 上游判 11133 |
| 5 | 目录补 `max_tokens` | `injectCatalogMaxTokens` | 仅在 body 没有时补 |
| 6 | 补 `stream_options.include_usage` | 内联 | 只在缺失时加 |
| 7 | `tool_choice` 归一 | `normalizeToolChoice` | `none` 连 `tools`/`functions` 一起删 |
| 8 | 工具参数里的 `\_` → `_` | `normalizeToolPatterns` | 与脱敏开关**无关**：这是"让请求通过" |
| 9 | `developer` → `system` | `normalizeRoles` | 只改这一个值；其它角色不合并/不重排/不删除 |
| 10 | `image_url` 字符串 → 对象 | `normalizeImageURL` | 空串/非法 url 不补默认值，让上游报真实错 |
| 11 | `stop` 字符串 → 数组 | `normalizeStop` | 上游只收数组 |
| 12 | 丢弃 `reasoning_text` 内容块 | `dropReasoningContentParts` | content 变空且无 tool_calls 时整条消息删除 |
| 13 | 合并相邻 tool_calls | `mergeAdjacentToolCalls`（`tool_pairing.go`） | **必须最先跑**（先合并再 repack） |
| 14 | tool 结果块重排 | `repackToolResultBlocks` | 只重排顺序，结果相对顺序不变 |
| 15 | 清理孤儿 tool_calls | `cleanupOrphanToolCalls` | 两侧对称裁剪；`function.name` 为空的 call 永不留（上游 11133） |
| — | 回写 `obj["messages"]` | 内联 | **无条件回写**，否则单独生效的 repack 会被原 slice 覆盖 |
| 16 | 注入思维链 | `injectThinking`（`thinking.go`） | deepseek 系；必须早于 17 |
| 17 | `reasoning_effort` 档位降级 | `normalizeReasoningEffort` | 就近档位；`off/none/disabled` 删字段；未知模型透传 |
| 18 | 回填 `reasoning_content` | `backfillReasoningContent` | deepseek 系；每条 assistant 补字符串 + 镜像 `reasoning` |
| 19 | 指纹脱敏 | `sanitizeMessages` | **仅** `sanitize==true` |
| 20 | 序列化 | — | 失败则原样返回入参 |

工具配对的三步对**所有模型**都跑，与 deepseek-only 的脱敏开关无关。空 content 的判定要把 `nil`、`""`、`[]any{}` 都算空。

## SSE：白名单重建，不是透传

`internal/upstream/sse.go` 逐帧重建：

- 顶层只保留 `id / object / created / model / system_fingerprint / service_tier`，缺失时补 `object="chat.completion.chunk"`、`id="chatcmpl-wb2api"`。
- `choices[].delta` 只重建 `role / content / reasoning_content / refusal`（仅非空字符串）、非空 `tool_calls`、非占位 `function_call`；`finish_reason` 空串 → `nil`。
- `usage` 缺失时写 `null`（**不是删键**）；存在则走 `normalizeUsageCacheAliases`。
- 透传前 `stripToolCallNames`：同一个 `index` 的 `function.name` 只出现一次，后续碎片帧不带 `name`。这是 OpenAI 客户端的兼容要求。
- 丢弃：空分隔行（本函数自产 `\n\n`）、`data: [DONE]` 之后的一切。
- 透传原文：`error` 帧（**唯一**绕过白名单的例外，否则客户端拿不到 `code/msg/requestId`）、非 JSON 的 `data:` 负载、其它非空行（注释）。
- 收尾**恰好一个** `data: [DONE]`；上游没给就补。
- 一帧有效数据都没有 → 写一条 `upstream_parse` 的 error 帧 + `[DONE]`，并返回 `errEmptyStream`。

非流式聚合 `Aggregate`：

- `usage` 取**最后一个**带它的帧，缺失时 `total = prompt + completion`。
- `reasoning_content` 从 `delta` 与整段 `message` 两处累积，非空才输出。
- content 有 latch 语义：只有非空文本才置位，整段 `message` 回退只在没置位时生效。
- 输出单个 `{"object":"chat.completion",...}`，`tool_calls` 按 index 排序。
- `finish_reason=="length"` 或未见 `[DONE]` → `dropTruncatedToolCalls`（参数非空却解析失败 = 截断；空串 = 合法的无参调用）。

空闲监控 `idle.go`：`monitorBody` 只在 `idle > 0` 时包一层；每次 `Read` 有数据就刷新时间戳；超过 idle 取消请求 context。首字节归 `Transport.ResponseHeaderTimeout` 管，流中停滞归这里管。

## DSML 工具标记还原

上游只在请求**声明了 `tools`** 时才会把标记解析成结构化 `tool_calls`。中转环节丢 `tools` 是常态，于是标记以纯文本落进 `content`，只认结构化调用的客户端（Codex 等）就当正文写进历史、回合直接结束。网关是最后一道防线。

标记形态（`dsml.go`）：分隔符 `markupPipe = "\uFF5C\uFF5C"` 是**两个**全角竖线 U+FF5C —— **不是**一个 `‖`（U+2016），也不是单根竖线。每个开始标记都有对应的 `/` 结束标记：

```go
// 形状（PIPE = markupPipe = "\uFF5C\uFF5C"）
//   <PIPE>DSML<PIPE> calls>          … </PIPE>DSML<PIPE> calls>
//   <PIPE>DSML<PIPE> invoke name="X"> … </PIPE>DSML<PIPE> invoke>
//   <PIPE>DSML<PIPE> parameter name="k" string="true">v </PIPE>DSML<PIPE> parameter>
const (
	markupOpen        = "<"  + markupPipe + "DSML" + markupPipe + " calls>"
	markupClose       = "</" + markupPipe + "DSML" + markupPipe + " calls>"
	markupInvokeClose = "</" + markupPipe + "DSML" + markupPipe + " invoke>"
	markupParamClose  = "</" + markupPipe + "DSML" + markupPipe + " parameter>"
)
```

**做夹具不要手打竖线**（本文件也刻意不再内联渲染字形）：从 `internal/upstream/testdata/leaked_markup.txt` 取真实样本，或用下面这段拼接出字节精确的完整块：

```go
p := "\uFF5C\uFF5C"
block := "<" + p + "DSML" + p + " calls>" +
	"<" + p + "DSML" + p + ` invoke name="read_file">` +
	"<" + p + "DSML" + p + ` parameter name="path" string="true">/etc/hosts` +
	"</" + p + "DSML" + p + " parameter>" +
	"</" + p + "DSML" + p + " invoke>" +
	"</" + p + "DSML" + p + " calls>"
```

手敲的 `‖`（U+2016）、单根竖线、或漏掉 `/` 的结束标记都不会命中修复器——测出来的"没修好"是假阴性。

解析器硬前提（`parseMarkupBlock` / `parseMarkupParams`）：块内除 `invoke` 与空白外不能有其它字节、每个 `invoke` 必须闭合、每个 `parameter` 必须闭合、`string="true"` 取值按原文、其余按 JSON 解析（失败退化为字符串）。

还原规则：

- 流式：`repairFrame` 把 `delta.content` 喂给 `MarkupRepair.Feed`，命中就额外产出 `delta.tool_calls` 帧（`index = maxToolIdx+1+n`），并把 `finish_reason` 延后到这些帧**之后**再发；源帧本来没有 finish 就**不补**一帧收尾。
- 非流式：`RepairAggregatedResponse` 仅在"该 choice 无结构化 tool_calls 且 `finish_reason=="stop"`"时改写，并把 finish_reason 改成 `tool_calls`。
- 两级校验：白名单非空 → 严格（名字必须在白名单）；白名单为空 → 弱判定（名字像标识符即可）。硬前提：块完整闭合、块内无正文夹杂、本轮没有结构化 tool_calls、非流式还要求 `stop`。
- 合成流式 call 的 `index` 必须是 `float64`：写成 int 会让两个调用在 JSON 里塌成一个。
- **绝不吞字节**：所有"拿不准"的分支都走原文回吐。参数值永远不丢。

## 出站身份与头族

`headers.go` 的职责是让请求在形态上等于官方客户端。要点：

- 通用必需：`X-CodeBuddy-Request: 1`（官方风控闸门头，**所有** API 请求必带）、`Accept: application/json`（不是 `*/*`）、`X-Requested-With`、按 realm 决定的 `Origin`/`Referer`、`X-Machine-ID`/`X-Session-ID`（由 uid 恒定派生，跨重启不变）。
- UA 三段式：`WorkBuddy/<ver> <brand>/<ver> CLI/<cli>`；第一段恒为 camel `WorkBuddy`，只有第二段随 realm 变（CN `WorkBuddy`、global `workbuddy-ai`）。billing/checkin 走另一套硬编码 `axios/1.16.1`。`X-IDE-Version` **必须**等于 UA 里的版本，不一致本身就是可判别指纹。
- 归属头：`X-Agent-Purpose: conversation` + `X-IDE-Name/Type/Product`；`client_name=="SaaS"` 时只发 `X-Product: SaaS`，不发 `X-IDE-*`。
- 会话三件套是**通道校验**的一部分：`X-Conversation-ID` / `X-Conversation-Request-ID` / `X-Conversation-Message-ID` 缺任一头即上游 400 `code=11-128`。另外 `X-Trace-ID` 与 request id **互不回落**（恒等会成为可判别痕迹），SpanId/ParentSpanId 必须独立随机。
- 企业号才注入身份头（`X-Auth-Method`、`X-Userinfo=base64(JSON{...})`）；个人号**零注入**——补空值反而是伪造指纹。
- `X-Device-Token` 优先级：凭证里 > 配置 > 文件（文件读取有 5min 缓存、>1KB 拒绝）。chat 与 billing 都要带。
- **安全红线：chat 请求绝不携带 `X-Refresh-Token`**（只在 `RefreshHeaders` 里用）。

## 指纹脱敏（`sanitize.go`）

不是语义过滤，是照上游黑名单做字面清除：

1. 廉价预检（`sanitizeFeatures`）：未命中任何特征就原样返回，零分配。特征含 `x-anthropic-billing-header`、`cc_entrypoint=`、`You are Claude Code`、Codex 指令开头、`11128` 等。
2. 删整段 `x-anthropic-billing-header: …;`。
3. 循环剥掉尾部裸 `cc_xxx=…;`。
4. 残留裸键名改写成 `x-anthropic-billing-hdr`（与第 2 步是两套不同语义，**不可合并成一个正则**）。
5. 单词级替换但保留语义；其中 `"11128"` → `"11-128"` 是反探测：请求体里出现裸 `11128` 会被整单拦截。

覆盖范围：`content`（字符串或 text 分块）、**`tool_calls[].function.arguments`**、`reasoning_content` 与 `reasoning`。漏掉 arguments 曾是真实盲区。开关是 `Client.SanitizeFingerprints`（atomic，默认 true），既可由配置也可由面板热改。

身份类规则（品牌词 `DeepSeek Harness` → `DeepSeekRuntime`）单独成组，作用域由 `features.sanitize_brand_scope` 控制：`all`（默认）= 所有消息角色；`identity` = 仅 `system`/`developer`，其余角色原样透传。实现上 `sanitizeTextWith(text, brand)` 区分两类规则，`sanitizeMessages(msgs, brandGlobal)` 逐条按 role 决定是否应用；`PrepareBodyOptWithEffortsDefaultAndCapsScope` 透传作用域，旧函数保持 `all` 语义。

## 出站头族与指纹（2026-10 同步，issue #51）

上游把「客户端形态不一致」当作渠道校验判据，因此头集合必须逐项对齐真机抓包，而不是「多一个头无所谓」。

| 规则 | 实现 | 为什么 |
|---|---|---|
| `/v3/config` UA | `v3ProbeUA` / `v3ProbeUALite` | CN 三段 `WorkBuddy/<v> WorkBuddy/<v> CLI/<c>`，global 两段 `WorkBuddy/<v> CLI/<c>`；旧 `CodeBuddyIDE/…` 在 WorkBuddy 分发包里不存在，是最显眼的非官方指纹 |
| chat 头剥离 | `ChatHeaders` | 不发 `Origin`/`Referer`/`Accept-Language`/`X-Machine-ID`/`X-Session-ID`；这些是 REST 通用层形态，桌面 chat 从不发 |
| 不发明哨兵头 | `ChatHeaders` / `injectGlobalChatHeaders` | 个人号两个方向都不发 `X-No-Enterprise-Id`/`X-No-Department-Info` |
| `Accept` | `ChatHeaders` | 只 `application/json`（官方仍收 SSE 流）；`Accept-Encoding` 刻意不设，否则关闭 net/http 透明解压 |
| `X-Private-Data` | `ChatHeaders` | 官方 chat 恒发 `true` |
| `X-Product` | `injectAttribution` | 恒 `SaaS`；产品名由 `X-IDE-Name/Type/Version` 承载（同名不同值比多头更易被规则命中） |
| 桌面任务链 | `DesktopChatWithExpert` | 一律走 `ChatHeaders`，不再手搓 20 多头（手搓是漂移根因） |
| 桌面上报指纹 | `desktopFingerprint` | `osVersion=10.0.28000`、`cpuModel`、`cpuCores=8`、`memorySize=16`；`qimei36` **仅 CN**（国际版真机不带） |
| `X-Domain` | `desktopHost` | 上报头发**裸主机名**，不是带 scheme 的 URL |
| `X-Request-ID` | `session.NewMessageID` | 32 hex 消息 ID 形态，不是十进制纳秒 |
| 会话 ID | `session.NewConversationID` | v4 UUID；**禁止** `wb2api-*` 自证前缀进任何出站字段 |

自证前缀曾散落 15 处（面板任务、排程活跃、旅行领养、夜猫子、校园活动、桌面会话）。新增出站 ID 一律用 `internal/session` 的两个生成器，不要再手拼 `fmt.Sprintf("wb2api-…")`。

## 三个 CN base 的分工（不可互换）

| base | 域名 | 承载 | 指纹/判据 |
|------|------|------|-----------|
| `ChatBaseCN` | `https://www.workbuddy.cn` | chat（SSE）、`/v3/config` 模型目录、growth 域（旅行 / streak）、**token 刷新**（`refreshOnce` / `doRefresh` 用 `chatBase` 拼地址）、桌面端 `/v2/report` 与 user-asset | 桌面端 `product.json` 的 endpoint；`X-Domain` 取账号落盘 domain，否则 chatBase host |
| `BillingBaseCN` | `https://www.codebuddy.cn` | 每日签到、余额查询、CLI 活跃上报、小程序上报 | CLI 指纹域，任务计分依赖，**不要跟着 chat 改** |
| 登录 | `https://copilot.tencent.com` | 仅 OAuth device flow（扫码 / 设备码） | 只出现在 `internal/panel/login.go`、`cmd/login`；token 刷新不走这里 |

`copilot.tencent.com` 是官方 `NetworkFailover` 里的 chat 备选入口：海外出口（Clash 全局 + TUN）
对它 TLS 握手直接复位，因此缺省主入口必须是 `www.workbuddy.cn`；需要时用 `cn.chat_base`
回填备选。`Client.chatBase()` 是 CN chat/目录/growth/token 刷新的单一出口，改主域只改
`ChatBaseCN` 与这个键，不要在端点层各拼一份。**注意 `cn.chat_base` 同时改变 token 刷新
入口**，这也是它必须无条件列入 `restartRequiredFields` 的原因之一。

## 端点清单（按域）

| 域 | 端点（方法 + 路径） | 方法 |
|----|--------------------|------|
| 对话 | `POST /v2/chat/completions` | `ChatStreamContext` |
| 凭证 | `POST /v2/plugin/auth/token/refresh` | `refreshOnce` / `RefreshToken` |
| 目录 | `GET /v3/config`、`GET /console/enterprises/personal/models` | `fetchV3ConfigModelMap` / `fetchEnterpriseModels` |
| 计费 | `POST /v2/billing/meter/get-user-resource`、`…/daily-checkin`、`…/get-enterprise-user-usage` | `UserResourceDetailedWithExpiry` / `DailyCheckin` / `enterpriseResource` |
| 领取 | `POST /billing/meter/claim-gift`、`claim-compensation`、`/billing/ide/trial` | `ClaimGift` / `ClaimCompensation` / `ClaimTrial` |
| 上报 | `POST /v2/report` | `ReportChatActivityModel` / `ReportMPEvent` / 桌面端变体 |
| 成长任务 | `GET/POST /v2/activity/growth/tasks`、`…/tasks/accept`、`…/tasks/{code}/claim` | `tasks.go` |
| 旅行 | `/activity/growth/buddy/{travel/status,travel/depart,travel/claim,info,first,agreement}` | `travel.go` |
| 连登/抽奖 | `/activity/growth/streak`、`/redeem`、`/lottery/summary`、`/lottery/draw` | `streak.go` |
| 其它 | `heatmap`、`makeup-cards/use`、`/portal/activity/school/vouchers`、`/v2/user-asset/appearance/set`、`/portal/operation-platform/market/expert/list`、`/console/account` | `blackcat.go` / `school.go` / `desktop.go` / `profile.go` |

调用辅助函数各有约定：`growthJSON` = chatBase + `BillingHeaders`；`billingJSON` = billingBase + `BillingHeaders`；小程序口径额外加 `X-Client-Platform: miniprogram`；web 口径设 `x-client-platform: web` + workbuddy.cn 的 Origin/Referer。新增端点请复用这些 helper，不要手拼 base/header。

事件上报必须带 `userId`（缺失时上游返回 200 但静默丢弃），且别用"最小 3 字段"——上游随时可能加严（`report.go` 头注释）。

## 刷新与单飞

`refresh_singleflight.go`：key 是 `uid|rt`，成功结果有 60s grace；**只有 refresh token 真的被轮换的成功结果**才进 grace 缓存（否则保活路径的连续 12153 计数永远涨不上去）。`refreshOnce` 是三段式：锁内取快照 → 锁外做网络 I/O（30s 上限）→ 锁内校验快照未变再写回（AND 语义）。`expiresIn` 超过 10 年视为脏数据。

## 测试约定

- 不用 golden 文件；SSE 直接写内联字符串（`data: {json}\n\n` … `data: [DONE]\n\n`）。
- 两种假上游：`rtFunc` + 最小 `Client` 字面量（只关心解析时）；`httptest.NewServer` + `newTestUpstream`（要断言线上 body/header 时）。
- 断言走 `streamFrames(t, raw)`、`summarize`/`assertSummary` 等既有 helper，不要新写一套解析。
- **会随代码一起改的"冻结断言"**：精确帧数、`[DONE]` 恰好一个、`normalizeFrame` 的逐字节 JSON、`finishIdx > lastCallIdx` 的先后顺序。改协议前先看这些断言。
- 真实泄漏样本放 `internal/upstream/testdata/leaked_markup.txt`，不要把它内联进测试。

## 反模式

- 在 `payload.go` 里插入一个"顺手"的改写步骤而不考虑它在表里的位置（尤其别抢在步骤 1 之前或步骤 13-15 之间）。
- 用透传代替 SSE 白名单，或把 error 帧也丢进白名单。
- 因为"看起来多余"就删掉 `X-CodeBuddy-Request` / `X-IDE-Version` / 会话三件套。
- 把脱敏写成 `strings.ReplaceAll` 精准替换（会破坏语义且漏 `tool_calls.arguments`）。
- 让 `Classify` 之外的地方再判一次错误码文本。
