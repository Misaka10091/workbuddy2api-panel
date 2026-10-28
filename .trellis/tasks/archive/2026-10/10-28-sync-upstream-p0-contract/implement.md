# 实施计划：同步上游 P0（出站契约 5.9.3 / 11-128 抢救 / 6020-6021 排队）

> 配套：[prd.md](./prd.md)（要什么）、[design.md](./design.md)（怎么做、为什么这样做）、`research/`（上游原文与证据）。
> 上游提交：`ee1c80d` `8ecf115` `47f1ed6`（契约）、`db31fc2` `4a577fc` `4f79fc8` `49f9db4` `e4dd458`（抢救）、`f07eba5`+`80e844f`（排队）。

## 0. 准备与纪律

> **必读证据（不进 inject 清单，按需自行 `read`）**：三份 research 各 38–54KB，超过 `context_injection.max_file_bytes` 且合计超过 `max_total_bytes`，强行入清单只会被静默截断（最关键的「移植清单」正好在文件尾部）。逐字正则、上游原文片段、本仓锚点行号都在里面，实现到哪一条就去读对应章节：
> - `research/upstream-outbound-contract-5.9.3.md` —— §2 五条路径头键集、§4 ID 形态、§5 条件头、§7 report、§8 本仓锚点/不能照搬、§10 移植清单
> - `research/upstream-11128-rescue.md` —— §1 @ 段正则、§2 身份句式正则、§3 enum、§4 三级抢救、§6 移植清单
> - `research/upstream-queue-6020-6021.md` —— §1 parseQueueRetryAfter、§2 轮转、§3 末端映射、§5 TTL、§6 移植清单/不要照搬

- [ ] 工具链：`go` 不在 PATH，用 `$env:PATH = "C:\tools\go\bin;$env:PATH"`；`internal/panel` 的 JS 测试需 `node`（缺则 `t.Skip`，skip ≠ 通过）。
- [ ] 建分支：`git checkout -b sync/upstream-p0-contract main`。
- [ ] 三条硬纪律（违反即返工）：
  - **不 `git apply` / 不 `cherry-pick` / 不整段 replace** —— 按「目标函数 + 语义」重写（两仓 merge-base 是 `dbd7c68`，签名已分叉）。
  - **不改容差、不删断言** —— 反转断言时先写清「反转后的语义」，再改。
  - **不 `git add -A`** —— `*.md` 全被忽略、`auths/` 是明文凭证、根目录有运行产物。
- [ ] 每个提交前的质量门：`go build ./... && go vet ./... && go test ./...`。

## 提交 1／3：`fix(upstream): 出站契约对齐 WorkBuddy 5.9.3`（改动集 ①）

> 改动面最大，建议先做「删多余头 → 补必发头 → 版本 → ID 形态 → report/billing → gzip → 测试」的顺序，每完成一组先跑 `go test ./internal/upstream/` 看既有断言变化，避免一次堆到 20 处红。

### 1.1 常量与版本
- [ ] `internal/upstream/headers.go`：`defaultClientVersionCN` → `5.9.3`；`defaultCliVersion` → `2.165.0`（global 版本号 `5.8.4` **不动**）。
- [ ] `internal/upstream/desktop.go`：CN 三元组 → `5.9.3 / d46924219e79332f49c37ccebf27292f87001b19 / 1793093056315`；global 三元组不动。
- [ ] `desktopUAFor`：CLI 段改用 `c.cliVersion()`（与上游不同，注释写明「让 `upstream.cli_version` 对 report 路径同样生效」）。

### 1.2 头族收敛（本提交的主体）
- [ ] `CommonHeaders`：删 `Origin`、`Referer`、`X-CodeBuddy-Request`、`Accept-Language`、`injectAccountStableHeaders` 五处。
- [ ] 删死代码：`injectAccountStableHeaders`、`deriveAccountStableID`、`originRefererFor`、`originRefererCN`（删前 `grep` 确认 `cmd/login` 的同名常量是另一份、不动）。`acceptLanguageFor` 若无调用一并删（`grep Accept-Language` + 相关测试）。
- [ ] `ChatHeaders`：末尾补 `injectCodeBuddyRequest(req)`、`X-Agent-Runtime: local-agent`、`injectBaggage(req)`；`X-Private-Data` 改 `privateDataHeaderValue(a)`（`IsEnterprise()` → `true`，否则 `false`）；`X-Domain` 改恒发 `c.chatHost(a)`。
- [ ] `injectConversationHeaders`：`X-Conversation-ID` / `acp-connection-id` 的派生与零值兜底改 v7；`X-Root-Request-ID` 支持 `meta.RootRequestID` 回落轮级。

### 1.3 ID 与身份头
- [ ] `internal/session/ids.go`：新增 `NewTimeOrderedID()`（UUIDv7，48bit 毫秒时间 + crypto/rand，熵源故障不 panic）。
- [ ] `internal/upstream/cache_key.go`：新增 `buildTimeOrderedUUID(uid, conversation, purpose)`（sha256 → 伪时间位，锚 `1767225600000` + 24 个月窗口）；`buildCacheKey`/`buildStableUUID` **保持 v4 不动**。
- [ ] `internal/auth/auth.go`：新增 `IdSource()`（读 JWT `id_source`）。
- [ ] `injectIdentityHeaders` 重写：`idSource` 条件头、个人号无身份来源时三头全不发、`token_source` 仅企业态进 `X-Userinfo`。
- [ ] `ChatMeta` 加 `RootRequestID` / `AgentType`；新增 `AgentTypeMain` / `AgentTypeAuxiliary`；`injectClientSDKHeaders` 加 `agentType` 参数；`server/handler.go` 填 `RootRequestID = session.RequestIDForKey("root:"+sessKey)`。
- [ ] 辅助链触发点：`grep -rn 'ChatStream(' internal/` 全量确认后，把面板判据对话、夜猫子（必要时含搜索探测）改为 `AgentType: AgentTypeAuxiliary`。

### 1.4 其余路径
- [ ] `doRefresh`：body → `strings.NewReader("{}")`；`RefreshHeaders` 补 `Accept`（宽松值）/ `X-Product` / `Authorization`（空则 `X-No-Authorization: true`）/ `X-Request-ID` / `X-User-Id` / `X-Domain`。
- [ ] `fetchV3ConfigModelMap`：删 `injectCodeBuddyRequest`、加 `X-Request-ID`。
- [ ] billing 族：`billingJSON` 注入 `X-Request-ID`（**不加进 `BillingHeaders`**，避免外溢 growth 域）；无 body POST 补 `{}`。
- [ ] `BillingBaseCN` 默认 → `https://www.workbuddy.cn`；`cmd/credit` 的硬编码同批切；`cmd/server/config.go` 加 `cn.billing_base` + `Default()`（空）+ `main.go` 装配 + 进 `restartRequiredFields` + `config.example.json` + README 字段表。
- [ ] `report.go` 重构：chatBase + 自建头族（抽 `reportHeaders(req, a)` 与 `desktop.ReportDesktopEvent` 共用）；字段集按 design §2.7 增删；`agentName=cli` / `agentType=main`；默认模型 `deepseek-v4.1-flash`。
- [ ] gzip：`Client` 加 `gzipMinChatBody` 字段（默认 1024）；`ChatStreamContext` 出站按阈值压缩 + 条件设 `Content-Encoding`；压缩失败静默发明文。

### 1.5 测试（本提交的验收凭据）
- [ ] 新增 `internal/upstream/cn_contract_test.go`：五条路径（chat / report / refresh / v3-config / billing）黄金头集，集合精确相等 + 值断言 + `absent` 反向断言；`transportOnly` 剔除六类。
- [ ] 新增 `TestCNIDShapes`：v7 正则、32 hex、消息级两头同值、`X-Trace-ID != X-Conversation-Request-ID`。
- [ ] 新增 `internal/upstream/headers_parity_test.go`：七组形态/条件断言（v7 稳定性与时间位落窗、`id_source`、根请求、`agent-type`、`private-data` 双分支、baggage 六键）。
- [ ] 反转既有断言（每条注明反转后语义）：`fingerprint_risk_test.go` 的 `X-Private-Data == "true"` → 按账号形态；`headers_conversation_test.go` 的 `Root == 轮级` → 会话级根；`client_test.go` 的 `billingBase == codebuddy.cn` → 新默认值；`device_token_test.go` 若以 `/v2/report` 当 billing 样本需换路径。
- [ ] 受 gzip 影响的既有测试（`sanitize_test` / `wire_integration_test` 里 ≥1KB 的 body）：把 `gzipMinChatBody` 设成极大值，而不是在测试里加解压。

**复查门（Gate 1）**：`go test ./internal/upstream/ ./internal/session/ ./internal/auth/ ./cmd/... -count=1` 全绿；`grep` 确认 `X-Machine-ID` / `X-Session-ID` / `Accept-Language` 在 `internal/upstream` 无出站注入点；确认 `cmd/login` 未被改动。

### 1.6 提交
```
fix(upstream): 出站契约对齐 WorkBuddy 5.9.3

根因：第一轮同步按当时口径把 X-Private-Data 写死 true、给 CommonHeaders 加了
X-Machine-ID/X-Session-ID 与全出站 X-CodeBuddy-Request，与 5.9.3 真机抓包相反。
改动：删除客户端从不发的头（Origin/Referer/Accept-Language/X-Machine-ID/X-Session-ID）、
补齐必发头（X-Agent-Runtime/baggage/refresh 四头/billing X-Request-ID）、版本切
5.9.3+CLI 2.165.0、ID 形态改 UUIDv7、report 改走 chatBase、chat body 按 1KB gzip。
取舍：billing 族 X-Request-ID 不进 BillingHeaders（避免外溢 growth 域）；global 版本
号保持 5.8.4（无抓包证据）。
```
（body 另需写「测试」「验证」，并按协作规范加 `Co-authored-by:` 尾注。）

## 提交 2／3：`fix(upstream,prompt): 11-128 抢救——@ 路径段/身份句式/system 拆句`（改动集 ②）

### 2.1 sanitize（加表项级，不动结构）
- [ ] `sanitizeFeatures` 加 `"Tokens prefixed with @"`（大小写敏感）。
- [ ] 新增 `@` 段整段正则常量（`(?is)`，尾部 `path (?:containing|that contains) spaces\.` 双分支）+ 改写串（**`project root`** 版）。
- [ ] `sanitizeTextWith` 里把该规则放在品牌块之后、hdr 剥离之前，**不在 `if brand` 内**（全局层）。
- [ ] 新增 `sanitizeIdentitySentenceRe`（七动词 + ≤3 修饰词窗口 + `${1}` 回填句尾空白）+ 改写目标 `You are a helpful AI assistant.`
- [ ] `hasIdentityFingerprint` 加动词字面量快路（ToLower+Contains）→ 正则复核。

### 2.2 payload
- [ ] `unescapePatternLiteralEscapes` 改名 `normalizeSchemaTree`，在既有遍历里插入 enum 补类型；新增 `inferEnumType`（integer 先于 number；混合/含 null 保持原样）。
- [ ] 注释钉住：`enum` 取值不动、`response_format` 不走这条（入口只有 `obj["tools"]`）。

### 2.3 prompt 三级抢救
- [ ] `prompt.go` 新增 `SplitSystemMessages`（只拆最长那条、句界切分、文本不改、失败返回 `(body,false)`）。
- [ ] `prompt.go` 新增 `MoveSystemToUser(body, neutral)`（拼接 `sysText+"\n\n"+txt`、首条 role 强制 `system`、9 条失败路径原样返回）。
- [ ] `server/handler.go` 起手形态与重试阶梯按 design §3.3 改：三层顺序 + 三个 `applied` 标志 + 三条日志串 + 每层成功后 `degrade.Trigger()` / `delete(tried, uid)` / `releaseHeld()` / `continue`。

### 2.4 测试
- [ ] sanitize：`@` 段「命中即改写」+ 两种措辞 + 跨行 + 嵌段保留前后文 + 幂等 + 段外 `@`（邮箱 / npm 包名 / Windows 路径）不动。
- [ ] 身份句式：8 个变体命中 + 4 类误伤守卫 + `brand=false` 不动。
- [ ] enum：能推断四类 + 推断不出保持原样 + `enum` 取值不变。
- [ ] prompt：`SplitSystemMessages`（只拆最长、单句不拆、多句拆对）；`MoveSystemToUser` 9 条失败路径；handler 层「先拆句、拆不动才搬家」的顺序断言。
- [ ] 每条新用例带中文 doc comment 说明「守护的语义 + 来源」（引用 11-128 / DSH / hub #677/#678 之类）。

**复查门（Gate 2）**：`go test ./internal/upstream/ ./internal/prompt/ ./internal/server/ -count=1` 全绿；确认 `probe_channel.go` **未**被引入。

### 2.5 提交
```
fix(upstream,prompt): 11-128 抢救——@ 路径段/身份句式/system 拆句

根因：DSH system prompt 的 @ 路径说明段是本仓唯一漏掉的 DSH 指纹（段内无品牌词，
身份规则不命中），原文原样发必 400；身份句只换品牌词而骨架仍在同样 400。
改动：补 @ 段整段改写（project root 口径）、身份句式整句中性化、枚举补推断类型、
11-128 抢救改三级（拆句留 system → 搬家 → 替换）。
取舍：拆句只拆最长一条（短 system 拆开废指令，真机 G 组 ARRR 0/3）；
probe_channel 不进主线。
```

## 提交 3／3：`fix(server,pool): 6020/6021 请求内轮转 + 429；11102 负缓存 TTL 收窄`（改动集 ③）

### 3.1 协议层
- [ ] `client.go` 新增 `parseQueueRetryAfter(raw)`（只解 `body.data.retry_after`，封顶复用 `retryAfterSanity`）。
- [ ] `ChatStreamContext` 头解析后加 `else if kind == ErrUpstreamBusy` 分支写 `ue.RetryAfter`。
- [ ] `ErrKind` 行尾注释改写为新语义（**含 2026-10-15 与 2026-10-28 两个实案的因果**，并明确「零惩罚不变」）；上游自己漏改的陈旧注释不要照抄。

### 3.2 路由层
- [ ] 轮转循环 `ErrUpstreamBusy` 分支：`applyErrorPolicy` → `fail` → `lastErr` 带 `RetryAfter` → `rotateBackoff` 失败 `break` → `continue`。
- [ ] 末端 `switch ue.Kind` 新增 `case ErrUpstreamBusy`：429 + `upstream_busy` + `retryAfterSec`（body 口径 `math.Ceil` → 5s 兜底 → 600 封顶），上游原文随后覆盖 `msg`。
- [ ] **`retryAfterSec := 0` 上移到 `var ue *upstream.Error` 之前**（否则编译不过）；补 `math` import。
- [ ] 首帧否决路径：注释写明「该路径无 `RetryAfter` 结构，落 5s 兜底」。
- [ ] `hint.go` 该 kind 文案去掉「switching accounts will not help」，同步 `hint_kinds_test.go` 断言。

### 3.3 池层
- [ ] `cooldown.go`：`modelBlockBaseTTL` → `2 * time.Minute`、`modelBlockMaxTTL` → `10 * time.Minute`（`modelBlockShift = 4` 不变），注释写清「为什么缩短」。
- [ ] `entry.go` 两处注释（`modelCooldown` 与"运行期字段不落盘"）同步 2m/10m。
- [ ] `persist.go` 恢复路径：`reason` 前缀 11102/11158 且 `Until > now+modelBlockMaxTTL` 的条目钳到 `now+max`（不碰 6004 墙钟）。

### 3.4 测试
- [ ] 改写 `handler_apilayer_test.go` 的 `TestChatQueueFullFailsFastWithRetryAfter` → 「两号各撞一次 6020 → 轮转 → 429 + `Retry-After: 5` + 零惩罚」，并**补调用次数断言**（原测试没断言 `calls`，是它当初没发现 fail-fast 的原因）。
- [ ] 新增 `TestChatQueueWaitingRetryAfterFromBody`（`data.retry_after=40` 端到端透传）。
- [ ] 新增 `TestParseQueueRetryAfter`（解析 + 封顶 + 非法值）。
- [ ] 新增 `TestModelBlockTTLShortEnoughToSelfHeal`（四条**区间**断言，不用等值断言）。
- [ ] 存量钳制：一条恢复路径用例（写 `now+6h` 的 11102 条目 → load → `Until <= now+10m`）。

**复查门（Gate 3）**：`go test ./internal/server/ ./internal/pool/ ./internal/upstream/ -count=1` 全绿；`go vet ./...` 通过；确认 `maxRetryAfterSec` / `retryAfterSanity` 复用而非新增重复常量。

### 3.5 提交
```
fix(server,pool): 6020/6021 请求内轮转 + 429；11102 负缓存 TTL 收窄

根因：2026-10-28 生产实案——CLI 遇 503 直接中断任务，两次即任务死亡；上游对
6020/6021 本身回 429，网关改写成 503 等于把「稍后再试」翻译成「服务不可用」。
改动：恢复请求内轮转（不冷却/熔断/喂连败），末端 429 + Retry-After（body 口径优先）；
11102 负缓存 base/max 6h/24h → 2m/10m 并在恢复时钳制存量条目。
取舍：这是对本地「容量信号 fail-fast」铁律的有意反转——事故成因是把排队当账号故障
罚号，本轮保留零惩罚，成因已切开。
```

## 验收（全量，提交 PR 前）

- [ ] `go build ./... && go vet ./... && go test ./...` 全绿；JS 测试若 skip 需在 PR 里注明「本机无 node，JS 未验证」。
- [ ] 逐条对照 prd.md「验收标准」1–7，缺项不许开 PR。
- [ ] 三条改动集的 commit 各自可 `git revert` 而无连带（自查：revert 后 `go build ./...` 仍通过）。
- [ ] 未向真实账号发任何请求（测试只打 `httptest`）；`git status` 无 `auths/`、`data/`、`config.json`、`wb2api.exe`。
- [ ] PR 说明包含：三处**行为变更**（chat 的 `X-Domain` 不再读 `auth.domain`；6020/6021 由 503 改 429；billing 域切 workbuddy.cn，`cmd/credit` 同批）。

## 回滚点

| 回滚目标 | 操作 | 影响 |
|---|---|---|
| 整个提交 1 | `git revert <sha>` | 回到 5.8.3 头族；`cn.billing_base` 配置键消失（老配置里若有该键会被当成未知键忽略，安全） |
| 只关 gzip | `Client.gzipMinChatBody` 设极大值 | 无需回滚提交 |
| 整个提交 2 | `git revert <sha>` | 回到「只换品牌词」的旧抢救；`prompt` 新增函数随之消失，无外部引用 |
| 整个提交 3 | `git revert <sha>` | 6020/6021 回到 503 fail-fast；11102 回到 6h/24h（存量已收窄的条目自然到期，不需反向迁移） |

## 后续候选（本轮不做，检查中发现的既有缺陷）

| # | 位置 | 缺陷 | 建议 |
|---|---|---|---|
| 1 | `internal/upstream/desktop.go` 的 `MarketExpertList` | `X-Domain` 仍设成带 scheme 的完整 URL（`c.chatBase(a)`），与提交 1／3 新立的「X-Domain 必须是裸主机名」自相矛盾 | 下一轮用现成的 `c.chatHost(a)` 一行修掉（既有缺陷，不属于本轮同步范围） |
| 2 | `internal/upstream/school.go` 的 `ReportMPEvent` | 直读 `c.BillingBaseCN` 而非 `billingBase(a)`，global 账号会打到 CN billing 域 | 既有行为，本轮只在注释写明；修它要连带确认 global 有没有对应端点，单独立项 |
| 3 | `internal/upstream` 的 v3-config 路径 | `X-Domain` 走既有 `v3ConfigDomain()`（含端口），与其它路径的裸主机名口径不一致 | 无抓包证据，暂不统一；黄金集用 `${HOST}` / `${HOSTNAME}` 两个占位如实反映 |
| 4 | `internal/server/backoff.go` 头注释 | 写着「TestMain 已置 0 加速轮转测试」，实际 `TestMain` 只关 `chatLogEnabled`，既有用例一直在真睡 500ms/1s | 单独立项（改成真话，或真的在 `TestMain` 里清零并审视其它用例的时序假设）。本轮的队列用例用局部 `fastRotation(t)` 清零，不碰 `TestMain` |
| 5 | `internal/upstream/client.go` 的 `ErrKind` const 块 | 把 `ErrUpstreamBusy` 尾注拆成独立 doc 块后，`ErrToolMismatch` 那行被 gofmt 当成独立 run（1 处对齐 nit） | 不是本仓门禁；`gofmt -w` 会洗掉 CRLF 并重排 18 行，故不做。将来若引入 gofmt 门禁再统一 |

## 落地时接受的偏离（写进 commit body，便于下轮同步识别）

| 处 | 偏离 | 理由 |
|---|---|---|
| `hint.go` 文案 | design 决策 8 要求改（去掉「switching accounts will not help」），上游没改 | 恢复轮转后那句会让运维误判「网关没在换号」；`hint_kinds_test.go` 断言同批改 |
| 队列用例的 body 选择 | implement.md 原写「两号各撞 6020」，实现用 **6021** 断言 5s 兜底 | 6020 真机恒带 `data.retry_after`，与「5s 兜底」断言自相矛盾；6020 → 40s 的口径由 `TestChatQueueWaitingRetryAfterFromBody` 单独钉住 |
| `pool.modelBlockBackoffReason` | 额外抽出的前缀判定helper（清除与钳制共用） | 11102/11158 前缀口径唯一化，等价重构、行为不变 |

## 反模式（实现时逐条避开）

- 用上游行号/hunk 直接 apply；把上游陈旧注释当设计意图抄进来。
- 为让测试变绿改容差、删断言、给 6020/6021 加罚号。
- 把 `X-Request-ID` 塞进 `BillingHeaders`（外溢 growth 域）。
- 把 `prompt_cache_key` 一起改 v7（存量缓存槽整体漂移）。
- 拆句时把所有 system 都拆（短人格句拆开会废掉指令）。
- 在 `internal/upstream` 里做换号决策，或在 `internal/server` 复制头族细节。
- 顺手改 `cmd/login` 的 `originRefererCN`。
