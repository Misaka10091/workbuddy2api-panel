# 技术设计：同步上游 P0（出站契约 5.9.3 / 11-128 抢救 / 6020-6021 排队）

> 依据：`research/upstream-outbound-contract-5.9.3.md`、`research/upstream-11128-rescue.md`、`research/upstream-queue-6020-6021.md`（三份均可离线复核上游原文）。
> 上游取证基线：`47f1ed6`（契约）、`e4dd458`（抢救）、`80e844f`（排队，内容等同 `f07eba5`）。

## 0. 约束（先看这三条，它们决定一切写法）

1. **不是祖孙关系**：本仓 HEAD 与上游同步基线 `5367e78` 的 merge-base 是 `dbd7c68`（2026-09-23），module path 也不同（本仓 `github.com/linguo2625469/...`，上游 `github.com/misakano7545/...`），`prepareBody` / `ChatStreamContext` 签名已分叉。→ **禁止 `git apply` / `cherry-pick` / 整段 replace**，一律按「目标函数 + 语义」重写。
2. **按函数名定位，不按行号**：本仓 Go 文件是 CRLF 工作区，行号在实现过程中会漂移；三份 research 里的行号只用于定位起点。
3. **分层不动**：出站头族 / 协议细节只在 `internal/upstream`；换号与末端映射只在 `internal/server` 的轮转循环；冷却状态只在 `internal/pool` 原语。任何"顺手"跨层都要先回到 [`backend/index.md`](../../../spec/backend/index.md:66) 的禁止清单核对。

## 1. 改动边界与落点

| 改动集 | 落点包 | 关键文件 | 是否碰对外契约 |
|---|---|---|---|
| ① 出站契约 | `internal/upstream`、`internal/session`、`internal/auth`、`internal/server`（仅填 `ChatMeta`）、`cmd/server`（配置面） | `headers.go` `client.go` `report.go` `desktop.go` `cache_key.go`、`session/ids.go`、`auth/auth.go`、`cmd/server/config.go`+`main.go` | 出站报文（上游视角）+ 新增配置键 `cn.billing_base` |
| ② 11-128 抢救 | `internal/upstream`（sanitize / payload）、`internal/prompt`、`internal/server`（抢救阶梯） | `sanitize.go` `payload.go`、`prompt/prompt.go`、`server/handler.go` | 出站 body 形态；`prompt` 包新增导出函数 |
| ③ 排队与 TTL | `internal/upstream`（新增解析）、`internal/server`（轮转 + 末端）、`internal/pool`（常量） | `client.go` `hint.go`、`server/handler.go`、`pool/cooldown.go` `pool/entry.go` `pool/persist.go` | **对外错误语义**：6020/6021 由 503 改 429 |

三个改动集互不依赖，可独立提交、独立回滚（见 §7）。

## 2. 改动集 ①：出站契约

### 2.1 常量与版本

| 常量 | 现值 | 目标 | 备注 |
|---|---|---|---|
| `defaultClientVersionCN`（`headers.go`） | `5.8.3` | `5.9.3` | 上游只对 CN 做过真机复核 |
| `defaultClientVersionGlobal` | `5.8.4` | **不动** | 无 5.9.3 的 global 抓包，不臆造 |
| `defaultCliVersion` | `2.161.0` | `2.165.0` | 两域共用单常量，**这是唯一被动波及 global 的改动**，commit message 要写明 |
| `originRefererCN` | `https://www.codebuddy.cn` | `https://www.workbuddy.cn` | 删除 `CommonHeaders` 的两行后本仓已无调用方 → **连常量与 `originRefererFor` 一起删**；`cmd/login/main.go` 的同名常量属于 OAuth 设备登录，**一字不动** |
| `desktopVersionCN` / `desktopCommitCN` / `desktopReleaseDateCN`（`desktop.go`） | `5.8.3 / db0e9b5… / 1792029498676` | `5.9.3 / d46924219e79332f49c37ccebf27292f87001b19 / 1793093056315` | global 三元组不动 |

### 2.2 头族收敛（本改动的核心）

`CommonHeaders` 从"统一塞满"改为"只发 RPC 层共有的最小集"，删五行：
`Origin`、`Referer`、`X-CodeBuddy-Request`、`Accept-Language`、`injectAccountStableHeaders` 调用。

`injectAccountStableHeaders` / `deriveAccountStableID` 上游保留定义但零调用；**本仓直接整段删除**（无调用方即死代码，留着会被下一轮误当能力）。

各路径头集（CN，剔除 `host/connection/content-length/accept-encoding/x-device-token/content-encoding` 六类传输头）：

| 路径 | 键数 | 独有必发 | 绝不应出现 |
|---|---|---|---|
| `/v2/chat/completions` | 38 | `x-codebuddy-request`、`x-agent-runtime`、`baggage`、会话三件套 + `acp-connection-id`、`x-private-data`、`x-agent-type`、stainless 四件 | `origin` `referer` `accept-language` `x-machine-id` `x-session-id` |
| `/v2/report` | 9 | `x-request-id`、`x-requested-with`、三段式 UA | `origin` `referer` `accept-language` `x-codebuddy-request` `x-device-token` |
| `/v2/plugin/auth/token/refresh` | 11 | `authorization` **与** `x-refresh-token` 双发、`x-auth-refresh-source=plugin`、`x-request-id`、`x-domain` | `origin` `referer` `accept-language` `x-codebuddy-request` |
| `/v2/billing/meter/*` | 7 | UA 是 `axios/1.16.1`（billing 层 axios 实例）、`x-request-id` | 同上四项 |
| `/v3/config` | 8 | `x-request-id`、`x-requested-with` | 同上四项（**且不得有 `x-codebuddy-request`**） |
| `/v2/chat/busy` | — | **本仓无此路径（`grep 'chat/busy'` 为空）** → 本轮不建 case、不新增 `ModelBusy` 客户端函数 | — |

取值要点：
- `X-Private-Data` 改 `privateDataHeaderValue(a)`：`a.IsEnterprise()`（`EnterpriseID != ""`）→ `true`，否则 `false`。**判据不是 `token_source`。**
- `X-Agent-Runtime` 恒 `local-agent`（仅 chat）。
- `baggage` 六键：`buddyId=,buddyName=`（恒空但保留 `k=` 形态）、`codebuddy.session_id=<X-Conversation-ID>`、`codebuddy.conversation_request_id=<X-Conversation-Request-ID>`、`x-agent-runtime=<同名头>`、`x-agent-intent=<同名头>`。头体同值是硬要求。
- chat 的 `X-Domain` 改**恒发** `chatHost(a)`（裸主机名），不再优先 `a.DomainValue()`。→ 行为变更：老 auth 文件里的 `domain` 字段对 chat 不再生效，PR 说明必须写明。
- `BillingHeaders` 的 `X-Domain` 保留 `DomainValue()` 优先、空则 `billingHost(a)`。

### 2.3 refresh / v3-config / billing 补齐

- `doRefresh`：body 由 `nil` 改 `strings.NewReader("{}")`（真机 `Content-Length: 2`）；`RefreshHeaders` 补 `Accept: application/json, text/plain, */*`、`X-Product: SaaS`、`Authorization: Bearer <at>`（空则 `X-No-Authorization: true`）、`X-Request-ID`、`X-User-Id`、`X-Domain`。
- `fetchV3ConfigModelMap`：删 `injectCodeBuddyRequest`、加 `X-Request-ID`。
- billing 族：**不往 `BillingHeaders` 里塞 `X-Request-ID`**——它在 `travel.go` / `tasks.go` 被复用于 chatBase 的 `/activity/growth/*`，那几条路径没有抓包证据。改为只在 billing 族入口（`billingJSON`）注入 `X-Request-ID`（纯随机 32 hex）。`growthJSON` 不动。
- `billingJSON` 的无 body POST 补 `{}`（真机 `Content-Length` 恒 2）。

### 2.4 gzip

- `ChatStreamContext` 出站：`len(prepared) >= c.gzipMinChatBody` 时压缩，成功才设 `Content-Encoding: gzip`，失败静默发明文。
- `gzipMinChatBody` 做成 **`Client` 字段**（默认 1024），不是配置项、不是包级常量：测试要能把它设成极大值等价关闭压缩（否则 `sanitize_test` / `wire_integration_test` 里 ≥1KB 的 body 会让 `io.ReadAll(r.Body)` 读到 gzip 字节）。
- 压缩级别 `gzip.BestSpeed`；响应方向不变（`Accept-Encoding` 仍刻意不设，避免关闭 net/http 透明解压）。

### 2.5 ID 形态

| 目标 | 形态 | 实现 |
|---|---|---|
| `X-Conversation-ID` 派生命中 / 零值兜底 | v7 | `buildTimeOrderedUUID(uid, src, "")` / `session.NewTimeOrderedID()` |
| `acp-connection-id` 派生命中 / 零值兜底 | v7 | `buildTimeOrderedUUID(uid, src, "acp")` / `session.NewTimeOrderedID()` |
| `prompt_cache_key` | **v4 不变** | 改 v7 会让存量会话的缓存槽整体漂移一次，收益为负 |
| `X-Request-ID` / `X-Conversation-Message-ID` / billing `X-Request-ID` / `X-Trace-ID` | 32 hex | 既有生成器 |
| `X-Root-Request-ID` | 32 hex，会话级根 | `session.RequestIDForKey("root:"+sessKey)`，无根回落轮级 |

`buildTimeOrderedUUID` 的时间位**不能取 `time.Now()`**（否则同 (账号,会话) 每轮换值、缓存槽全废），用 sha256 派生的伪时间：锚 `1767225600000`（2026-01-01Z）+ 24 个月窗口；`u[6]` 强制 `0x70`、`u[8]` 强制 `0x80`。

### 2.6 可选头条件化

- `auth.IdSource()`：读 JWT `id_source` 声明（复用既有 `jwtClaim`）。
- `injectIdentityHeaders` 重写：`idSource != ""` → 发 `X-Id-Source`；`enterprise == "" && idSource == ""` → 三个身份头全不发；`token_source` **只进企业态的 `X-Userinfo`**（个人号的 token_source 是来路不是身份）；`X-Userinfo` 三段键（`uin` / `owner_uin` / `id_source` / `token_source`）条件拼接，缺维度不写空串。
- `ChatMeta` 加 `RootRequestID` 与 `AgentType`；`injectClientSDKHeaders` 多一个 `agentType` 参数；新增 `AgentTypeMain` / `AgentTypeAuxiliary`（空 = `main`）。
- `AgentTypeAuxiliary` 的触发点（本仓）：`panel/autotask.go` 判据对话、`upstream/blackcat.go` 夜猫子；`server/searchtool.go` 未走 `ChatStreamContext`，**实现时先全量 `grep 'ChatStream('` 确认**，不要凭上游清单硬套。

### 2.7 `/v2/report` 与 billing 域

- `/v2/report` 改走 `chatBase` + 自建头族（Accept / Content-Type `;charset=UTF-8` / `X-Requested-With` / `X-Request-ID` / `X-Product` / `desktopUAFor` / `Authorization` / `X-User-Id` / `X-Domain`），**不带 `X-Device-Token`**。
- 字段集：`desktopFingerprint(a)` 打底 + merge `isMaxMode/source/inputAttemptId/vcsType/vcsRepo/vcsBranchName/vcsRevId/traceId`、`buddyId=""`、`buddyName=""`、`codebuddy.*` 两键；**删** `maxToken/maxSteps/temperature/maxRetries/skillCount/totalCount/fileUri/expertId`；`agentName=cli`、`agentType=main`；默认模型 `deepseek-v4.1-flash` / `DeepSeek-V4.1-Flash`。
- 本仓 `desktop.go` 的 `ReportDesktopEvent` 已走 `chatBase + desktopUAFor`，与新的 report 头族重复 → **抽一个 `reportHeaders(req, a)` 共用**，避免两处再次漂移；顺带补 `X-Requested-With: XMLHttpRequest`。
- `BillingBaseCN` 默认改 `https://www.workbuddy.cn`；新增配置 `cn.billing_base`（空 = 新默认）。

### 2.8 契约测试

新增 `internal/upstream/cn_contract_test.go`（**只建 CN**，本仓 global 侧有 `/console/chat/completions` 回退，单独建会放大维护面）：

- 数据结构 `cnGolden{path, headers []string, values map[string]string, absent []string}`，五条 case（chat / report / refresh / v3-config / billing）。
- 假上游 `httptest` + **生产函数真实发请求**，server 侧 `r.Header.Clone()` 抓头；比较前剔除 `transportOnly` 六类。
- 三类断言：集合精确相等（`extra`/`missing` 分别报错）、`values` 逐键等值、`absent` 逐键为空。
- 附带 `TestCNIDShapes`：v7 正则、32 hex、消息级两头同值、`X-Trace-ID != X-Conversation-Request-ID`。

新增 `internal/upstream/headers_parity_test.go`：v7 派生稳定性与解时间位落窗口、`id_source` 条件头、根请求、`agent-type`、`private-data` 双分支、`baggage` 六键一致性。

需反转/新增的既有断言清单见 research §9（`fingerprint_risk_test.go` 的 `X-Private-Data`、`headers_conversation_test.go` 的 `Root == 轮级`、`client_test.go` 的 `billingBase`）。

## 3. 改动集 ②：11-128 抢救

### 3.1 sanitize 两条新规则（都是加表项级改动，不动 `hasFingerprint` / `sanitizeMessages` 结构）

**① `@` 路径段**（`sanitize.go`）：

- 预检串进 `sanitizeFeatures`：`"Tokens prefixed with @"`（大小写敏感，DSH 原文固定大小写）。
- 整段正则（`(?is)`，尾部双分支吃掉两种线上措辞）：
  `Tokens prefixed with @ are paths the user explicitly referenced\..*?path (?:containing|that contains) spaces\.`
- 改写串用 **`project root`** 版（`workspace root` 与 hub #677/#678 的结论冲突，`project root` 在两种说法下都安全且与 DSH 语义等价）。
- 该规则属**全局改写层**：放在品牌块之后、hdr 剥离之前，**不在 `if brand` 分支内**（上游不分角色匹配，保留原文必 400）。
- 段外 `@`（邮箱 / npm 包名 / Windows 路径）靠「字面量预检 + 整句锚点」零误伤；规则幂等。

**② 身份句式整句中性化**：

- 正则：`(?i)You are\s+(?:an?\s+|the\s+)?(?:[\w.-]+\s+){0,3}?(?:agent|assistant|model|AI|LLM)?[\s,]*(?:powered by|built on|built by|created by|developed by|made by|trained by)[^\n]*?\.(\s|$)` → `You are a helpful AI assistant.${1}`。
- `${1}` 回填句尾空白——RE2 无 `(?=)` 的绕法。
- 修饰词窗口上界 3 是防误伤的关键：`You are free to use tools built by …` 这类分词修饰宾语的句子必须不动（四条守卫用例覆盖）。
- 预检走「动词字面量快路（ToLower + Contains）→ 正则复核」：只按 `powered by` 判真会把讲解产品的普通消息也拖进清洗。
- 受既有 `features.sanitize_brand_scope` 约束（本仓表示为 `SanitizeBrandAll atomic.Bool`），**不新增配置项**。已核对：七种动词在本仓 `*.go` 零命中，57 条既有 sanitize 测试不会被打破。

### 3.2 payload：enum 补 type

- `unescapePatternLiteralEscapes` 改名 `normalizeSchemaTree`（它现在干两件事），在其既有遍历里插入 enum 补类型（`patternProperties` 之后、递归之前）。
- `inferEnumType` 判定顺序：全整数 → `integer`、含小数 → `number`、全布尔 → `boolean`、全串 → `string`；混合标量 / 含 `null` → **保持原样**（不猜）。
- `enum` 取值一字不改；`response_format` 天然不走这条（入口只有 `obj["tools"]`），实现时加一句注释钉住这一点。
- 调用点影响面：本仓实现在 `PrepareBodyOptWithEffortsDefaultAndCapsScope`（`payload.go:142`），生产调用 2 处、测试 59 处走短签名包装 → **本改动不改签名，无需动任何调用点**（与上游 #104 的教训相反）。

### 3.3 prompt 三级抢救

`internal/prompt/prompt.go` 新增（需加 `strings` import）：

| 函数 | 语义 | 关键约束 |
|---|---|---|
| `SplitSystemMessages(body) ([]byte, bool)` | 把**最长的那条** system 按句界拆成多条 system | 文本一字不改、承载仍是 system；只拆最长一条（短 system 拆开会废掉指令 = 真机 G 组 ARRR 0/3）；拆不动返回 `(body, false)` |
| `MoveSystemToUser(body, neutral) ([]byte, bool)` | 单句 system 时把原文搬进首条 user，system 换成中性提示词 | 拼接固定 `sysText + "\n\n" + txt`；首条 role 强制写 `system`；9 条失败路径全部原样返回 |
| （既有 `Rewrite`） | 整体替换 | 第三层，不变 |

`server/handler.go` 的落点与顺序：

- 降级期内起手形态（`PromptMode∈{passthrough,append}` 且 `degrade.Active()`）：拆句 → 搬家 → 替换，依次尝试并各自置 `splitApplied` / `movedApplied` / `degradedApplied`。
- 撞 `ErrContentBlocked` 后的重试阶梯：同一顺序，每层成功即 `degrade.Trigger()` + `delete(tried, uid)` + `releaseHeld()` + `continue`。
- 三条日志串（可直接 grep 现网行为）：`… -> split system into sentences, retry` / `… -> moved system text into first user message, retry` / 既有 `… -> degraded prompt retry`。
- `degradeGate` 只有「窗口」语义（`Trigger` 置到次日 00:00 CST），**没有层级状态**：降级期每次请求重跑那段 if/else 链，拆不动自然掉到下一层。不要新加持久化层级状态。

### 3.4 不移植

`internal/probe/probe_channel.go`（`//go:build ignore` + 独立 `package main`，手动 `go run` 才联网）——本仓无 `internal/probe/`，本轮不引入。若将来要复查真机行为，单独立项。

## 4. 改动集 ③：排队与 TTL

### 4.1 `ErrUpstreamBusy` 语义变更（这是对既有 spec 铁律的有意反转）

| 维度 | 现状（本仓） | 目标 |
|---|---|---|
| 换号 | 否，fail-fast 503 | **是**，请求内轮转，`rotateBackoff` 失败才 `break` |
| 罚号 | 否 | **否（不变）**：`applyErrorPolicy` 该分支仍零动作，不冷却 / 不熔断 / 不喂连败 |
| 末端 | 503 `upstream_busy` + `Retry-After`（兜底 5s） | **429** `upstream_busy` + `Retry-After`，上游原文仍覆盖 `message` |

**为什么可以反转**：本地 spec 的「fail-fast」铁律是为了避免 2026-10-15 的整池停摆，那次事故的成因是**把排队当账号故障去罚号**，不是"换号"本身。上游这次恢复轮转同时保留「零惩罚」，成因已被切开。依据（2026-10-28 生产实案）：WorkBuddy CLI 对 503 的处理是**中断任务**，两次 503 任务即死；排队可用性是**间歇**的（实案 33 秒内同模型恢复），fail-fast 把几秒抖动直接交给客户端。队列按 `client_user_type` 全局共享，换号绕不过排队——轮转的价值是"间歇空窗不再逐请求致命"，代价是 `MaxRotate` 次上游调用（客户端按 `Retry-After` 重试的节奏下有界）。

这段理由必须写进 `ErrKind` 注释与轮转分支注释（含两个实案日期），否则下一轮会被照着旧 spec 改回去。

### 4.2 实现要点（本仓结构，非上游 diff）

1. `upstream/client.go` 新增 `parseQueueRetryAfter(raw) (time.Duration, bool)`：只解 `body.data.retry_after`（int64 秒），`<=0`/非法 → `(0,false)`，封顶复用既有 `retryAfterSanity`（2h）；挂到 `*upstream.Error.RetryAfter`。调用点在 `ChatStreamContext` 头解析之后新增 `else if kind == ErrUpstreamBusy` 分支。
2. `server/handler.go` 轮转循环 `ErrUpstreamBusy` 分支：`applyErrorPolicy`（零动作）→ `fail`（沿用上游，不因排队偏离分叉面）→ `lastErr = &upstream.Error{Kind, Status, Msg: 完整 respBody, RetryAfter}` → `rotateBackoff(i, ctx)` 失败则 `break` → `continue`。
3. 末端 `switch ue.Kind` 新增 `case ErrUpstreamBusy`：`429` + `code=upstream_busy` + `msg` 兜底文案 + `retryAfterSec`（body 口径 `math.Ceil` → 缺省 5 → 封顶 `maxRetryAfterSec` 600），随后统一让上游原文覆盖 `msg`（遵守「不替换 `message`」硬约定）。
4. **隐性必改点**：`retryAfterSec := 0` 当前声明在 switch 之后，新 case 写不到它 → 必须上移；`math` import 需要新增。
5. 首帧否决路径（`vetoedStreamFrame` 那条）没有 `uerr`，`RetryAfter` 为 0 → 落 5s 兜底，注释写明。

### 4.3 11102 TTL

`modelBlockBaseTTL 6h → 2m`、`modelBlockMaxTTL 24h → 10m`，`modelBlockShift = 4` 不变（`base<<4 = 32m > 10m`，阶梯仍能走到封顶）。`entry.go` 两处注释同步（否则注释与常量自相矛盾）。新增 `TestModelBlockTTLShortEnoughToSelfHeal`，用**区间断言**（`>5m` / `>15m` / `max<base` / `base<<shift<max`）而不是等值断言，将来微调 3m/12m 不会红。

存量 `state.json` 里升级前写入的 11102/11158 条目 `Until` 可能是 `now+6h`：在恢复路径（`persist.go` 的 load）对 `reason` 前缀命中的条目把 `Until` 钳到 `now+modelBlockMaxTTL`，否则"上线即时收窄"要等 6h 才成立。钳制只作用于 11102/11158，不碰 6004 墙钟。

## 5. 兼容性矩阵

| 对象 | 影响 | 处理 |
|---|---|---|
| 老 `auths/*.json`（`domain` 字段） | chat 的 `X-Domain` 不再读它 | 接受（真机恒 chat host），PR 说明写明；不回写文件 |
| 存量 `state.json` | 11102 条目 TTL 可能是 6h | load 时钳制（§4.3） |
| `config.json` | 新增 `cn.billing_base` 键 | 缺省空 = 新默认；**加进 `restartRequiredFields`**（装配期写入） |
| `upstream.client_version` / `cli_version` 覆盖 | 仍生效 | `desktopUAFor` 保持上游「直接用 `defaultCliVersion`」会让 report 路径无视 `cli_version` 覆盖——**本仓改为走 `c.cliVersion()`**，注释标明与上游的差异 |
| 面板 `/status`、`usage.json`、OpenAI 响应体 | 不变 | 只增不改 |
| 对外错误码 | 6020/6021 由 503 → 429 | 属有意的对外语义变更，写进 commit message 与 spec |

## 6. 关键决策（research 里挂起的六项，本轮裁决）

| # | 决策点 | 裁决 | 理由 |
|---|---|---|---|
| 1 | `BillingBaseCN` 切 workbuddy.cn 与 `cmd/credit` | **同批切**，并加 `cn.billing_base` 逃生门 | 两处口径不一致比"独立命令保持旧值"更危险：查积分与实际计费走不同域会得到互相矛盾的余额 |
| 2 | chat 的 `X-Domain` 丢弃 `DomainValue()` | **丢弃** | 真机 661 条带 Origin 的请求全是 workbuddy.cn，`codebuddy.cn` 只是 billing API 主机名 |
| 3 | `X-Request-ID` 是否进 `BillingHeaders` | **不进**；只在 billing 族入口注入 | growth 域（`tasks`/`travel`）无抓包证据，外溢等于制造新的形态偏差 |
| 4 | `/v2/chat/busy` 轻头族 | **不做** | 本仓无该路径；不做无证据的"顺手补" |
| 5 | gzip 阈值 | **`Client` 字段**（默认 1024） | 测试需要能等价关闭；做成配置项会把测试细节泄漏成用户面 |
| 6 | `X-Userinfo` 的 `token_source` 收紧 | **采纳上游**：`token_source` 仅企业态，个人号带 `id_source` 时发 `{uin, id_source}` | 个人号的 token_source 是来路不是身份 |
| 7 | 排队失败是否解绑粘性 | **沿用 `fail()`** | 与账号健康无关但解绑不产生正确性问题；第一版不偏离上游以压缩分叉面 |
| 8 | `hint.go` 文案（"switching accounts will not help"） | **改**，并同步 `hint_kinds_test.go` 断言 | 改成轮转后这句话会让运维误判「网关没在换号」 |
| 9 | 存量 11102 条目钳制 | **做**（§4.3） | 否则收窄效果延迟 6h |
| 10 | `internal/probe` | **不移植** | 需手动 `go run` 的真机探针，不进主线 |

## 7. 交付与回滚

**分支**：`sync/upstream-p0-contract`（`<type>/<kebab>`，type 与 commit type 同一套；`main` 只接 PR merge commit）。

**三个提交，按此顺序**（互相独立，可单独 revert）：

1. `fix(upstream): 出站契约对齐 WorkBuddy 5.9.3` → 改动集 ①
2. `fix(upstream,prompt): 11-128 抢救——@ 路径段/身份句式/system 拆句` → 改动集 ②
3. `fix(server,pool): 6020/6021 请求内轮转 + 429；11102 负缓存 TTL 收窄` → 改动集 ③

**回滚点**：每个提交自包含（含其测试与断言反转），`git revert <sha>` 即可回到上一形态。改动集 ① 内部若只想回退某一条（如 gzip），把 `gzipMinChatBody` 设到极大值等价关闭，不需要回退整个提交。

**发布顺序建议**：③ → ② → ①。理由：③ 修的是现网致命错误（503 打死任务），② 修的是现网必现 400，① 是形态收敛、影响面最大且需要真机复核，放最后可独立观察。

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 头级改动方向错要等下一轮真机抓包才能证伪 | 黄金头集测试固化；每条改动在代码注释里写明证据（提交号 + 抓包条数） |
| gzip 引入新失败面 | 阈值保守（≥1KB 才压）、压缩失败静默发明文、`Client` 字段可关 |
| 6020/6021 轮转放大上游调用 | 沿用既有 `MaxRotate`；只在间歇空窗受益；不改变排队本身 |
| 三级抢救的降级态是进程级 | 降级只在"本就会失败"的请求上触发一次；三条日志串便于线上统计 |
| `cn.billing_base` 改变 token 刷新以外的全部计费入口 | 与 `cn.chat_base` 同规格进 `restartRequiredFields`；空值 = 新默认，旧配置零改动 |
| 断言反转时"改成通过"而不是"改成正确" | 每条反转的断言在 implement.md 里写明「反转后的语义」；反模式清单禁止改容差/删断言 |

## 9. 需要同步的 spec（Phase 3.3 执行）

| 文件 | 位置 | 现状 → 目标 |
|---|---|---|
| [`spec/backend/error-handling.md`](../../../spec/backend/error-handling.md) | 分类顺序段的 6020/6021 行、处置矩阵行、铁律 #2、反模式末两条 | 「fail-fast 不轮转 503」→「请求内轮转 + 429 + Retry-After，仍不罚号」 |
| [`spec/backend/upstream-protocol.md`](../../../spec/backend/upstream-protocol.md) | 「出站身份与头族」通用必需项、5.9.3 前的头族表、`X-Private-Data: true`、`X-Machine-ID/X-Session-ID`、「三个 CN base 的分工」 | 改为 5.9.3 口径；`BillingBaseCN` 改 workbuddy.cn；删「所有 API 请求必带 `X-CodeBuddy-Request`」 |
| [`spec/backend/config-and-hot-reload.md`](../../../spec/backend/config-and-hot-reload.md) | `restartRequiredFields` 清单与缺口表 | 加 `cn.billing_base` |
| [`spec/backend/pool-lifecycle.md`](../../../spec/backend/pool-lifecycle.md) | 冷却矩阵 `11102/11158` 行（"6h 起 ×2，封顶 24h"）与「运行期字段不落盘」里的「从 6h 基数重新学习」 | 两处都改 2m/10m，漏一处就又成了自相矛盾 |
