# Research: 上游 WorkBuddy 5.9.3 出站契约三提交技术档案

- Query: 提取 `ee1c80d` / `8ecf115` / `47f1ed6` 的确切改动，产出可照着移植的技术档案
- Scope: mixed（上游 git 对象离线读取 + 本仓代码只读比对）
- Date: 2026-10-28
- 上游取证基线：`47f1ed6`（三提交的最后一个；`5367e78..upstream/main` 里另两个 `f07eba5`/`80e844f` 与本主题无关）
- 引用约定：`path:line @47f1ed6` = 该上游提交里的位置；`path:line`（无 @）= 本仓 HEAD `b947f99` 的位置

---

## 0. 结论速览

1. **净效果**：CN 侧出站契约从「WorkBuddy 5.8.3 / CLI 2.161.0」整体切到「**5.9.3 / CLI 2.165.0**」，并把头集合做双向收敛——删掉客户端从不发的头（Origin/Referer/Accept-Language/X-Machine-ID/X-Session-ID/多余 X-CodeBuddy-Request），补齐客户端必发的头（X-Agent-Runtime、baggage、X-Request-ID 之于 refresh/v3-config/billing）。
2. **global 侧**：版本号 `defaultClientVersionGlobal` 仍 `5.8.4` 未动（上游明说"只对国内 5.9.3 做了真机复核"），但 `defaultCliVersion` 是**两域共用单常量**，所以 global UA 的 CLI 段被动从 2.161.0 变成 **2.165.0**。这是唯一波及 global 的改动。
3. **六条路径的黄金头集已固化**（`cn_contract_test.go` 的 `cnGoldenCases`），键数依次 38 / 9 / 8 / 11 / 7 / 8，且断言「集合精确相等」+「某些头绝不应出现」。
4. **一个必须先知道的事实**：本仓 HEAD 与上游基线 `5367e78` **不是祖孙关系**（merge-base = `dbd7c68`），module path 也不同（本仓 `github.com/linguo2625469/...`，上游 `github.com/misakano7545/...`），`prepareBody` 与 `ChatStreamContext` 的签名都已分叉。→ 这批改动**只能逐条手工移植**，不能 `git apply` / `cherry-pick`。
5. **本仓已有、上游没有的东西**（照搬时会冲突）：`b3` / `traceparent` 双链路头（`headers.go:442-443`）、`X-Conversation-ID` 的 36 字符 UUID 兜底（`headers.go:416`）、无静态 API Key 形态（已删 `X-API-Key`）。

---

## 1. 版本常量与 UA 拼装

### 常量（`internal/upstream/headers.go`）

| 常量 | 上游旧 | 上游新（@47f1ed6） | 本仓现值 |
|---|---|---|---|
| `defaultClientVersionCN` | 5.8.3 | **5.9.3**（headers.go:26） | 5.8.3（headers.go:25） |
| `defaultClientVersionGlobal` | 5.8.4 | 5.8.4（未动） | 5.8.4（headers.go:26） |
| `defaultCliVersion` | 2.161.0 | **2.165.0**（headers.go:31） | 2.161.0（headers.go:29） |
| `originRefererCN` | https://www.codebuddy.cn | **https://www.workbuddy.cn**（headers.go:41） | codebuddy.cn（headers.go:31） |

上游注释明确写了取值来源：分发包 `resources/install-manifest.json` 的 `appVersion` + `cli/product.json` 的 `genieVersion`。

### 四个 UA 函数的实际输出（CN / global 各一段）

| 函数 | 位置 @47f1ed6 | CN 5.9.3 | global 5.8.4 |
|---|---|---|---|
| `clientVersion(a)` | headers.go:56 | `5.9.3` | `5.8.4` |
| `cliVersion()` | headers.go:67 | `2.165.0` | `2.165.0`（共用） |
| `defaultWorkBuddyUAFor(a)` / `userAgent(a)` | headers.go:94 / :161 | `WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0` | `WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.165.0` |
| `uaBrandFor(a)` | headers.go:114 | `WorkBuddy`（首段恒驼峰） | `workbuddy-ai` |
| `v3ProbeUA(a)` | headers.go:136 | `WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0`（三段） | `WorkBuddy/5.8.4 CLI/2.165.0`（两段） |
| `v3ProbeUALite(a)` | headers.go:153 | `WorkBuddy/5.9.3 CLI/2.165.0` | 同左式（5.8.4） |
| `desktopUAFor(a)` | desktop.go:77 | `WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0` | `workbuddy-ai/5.8.4 workbuddy-ai/5.8.4 CLI/2.165.0` |

```go
// headers.go:94-97 @47f1ed6
func (c *Client) defaultWorkBuddyUAFor(a *auth.Auth) string {
	ver := c.clientVersion(a)
	return "WorkBuddy/" + ver + " " + uaBrandFor(a) + "/" + ver + " CLI/" + c.cliVersion()
}
```

```go
// desktop.go:77-82 @47f1ed6 —— 注意与 userAgent 的差别：首段也随 realm 换品牌，
// 且直接用 defaultCliVersion（不走 c.cliVersion()，config 覆盖在此路径不生效）
func desktopUAFor(a *auth.Auth) string {
	ver, _, _ := desktopTriple(a)
	brand := uaBrandFor(a)
	cli := defaultCliVersion
	return brand + "/" + ver + " " + brand + "/" + ver + " CLI/" + cli
}
```

### desktop 三元组（`/v2/report` 的包指纹）

```go
// desktop.go:63-68 @47f1ed6
desktopVersionCN         = "5.9.3"
desktopCommitCN          = "d46924219e79332f49c37ccebf27292f87001b19"
desktopReleaseDateCN     = int64(1793093056315)
desktopVersionGlobal     = "5.8.4"     // 未动
desktopCommitGlobal      = "73952edb48da46bd0b244661efe22f4f247c4977"
desktopReleaseDateGlobal = int64(1792063084107)
```

本仓对应：`desktop.go:50-52` 仍是 `5.8.3 / db0e9b51b7fa0b383f41f5b0b83dd442c8557918 / 1792029498676`。

---

## 2. 各路径精确头键集（直接抄 `cn_contract_test.go` 的黄金值）

测试文件头注释（`cn_contract_test.go` 顶部）说明了黄金集口径：取该路径**全部样本的交集**，剔除 `host / connection / content-length / accept-encoding / x-device-token` 五类，另把 `content-encoding` 列为「按 body 大小才加」的豁免。`var cnGoldenCases` 在 `cn_contract_test.go:42`。

### 2.1 `/v2/chat/completions`（38 头，真机 idx 438）

```
accept, authorization, acp-connection-id, b3, baggage, content-type, traceparent,
user-agent, x-agent-intent, x-agent-purpose, x-agent-runtime, x-agent-type,
x-b3-parentspanid, x-b3-sampled, x-b3-spanid, x-b3-traceid, x-codebuddy-request,
x-conversation-id, x-conversation-message-id, x-conversation-request-id, x-domain,
x-ide-name, x-ide-type, x-ide-version, x-private-data, x-product, x-request-id,
x-requested-with, x-root-request-id, x-trace-id, x-user-id,
x-stainless-arch, x-stainless-lang, x-stainless-os, x-stainless-package-version,
x-stainless-retry-count, x-stainless-runtime, x-stainless-runtime-version
```

关键值：`accept=application/json`、`x-private-data=false`、`x-agent-runtime=local-agent`、`x-agent-type=main`、`x-codebuddy-request=1`、`x-ide-version=5.9.3`、`x-product=SaaS`、`user-agent=WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0`。
**绝不应出现**：`origin, referer, accept-language, x-machine-id, x-session-id`。

### 2.2 `/v2/report`（9 头，idx 443）

```
accept, authorization, content-type, user-agent, x-domain, x-product,
x-request-id, x-requested-with, x-user-id
```
值：`accept=application/json, text/plain, */*`、`content-type=application/json;charset=UTF-8`、`x-requested-with=XMLHttpRequest`、`x-product=SaaS`、UA 三段式。
**绝不应出现**：`origin, referer, accept-language, x-codebuddy-request, x-device-token`。

### 2.3 `/v2/chat/busy`（8 头，idx 307/531/380…共 19 条）

```
accept, authorization, content-type, user-agent, x-domain, x-product,
x-request-id, x-user-id
```
值：`accept=application/json, text/plain, */*`、`content-type=application/json`、`x-product=SaaS`、UA 三段式。
**绝不应出现**：`x-codebuddy-request, x-conversation-id, x-conversation-request-id, acp-connection-id, baggage, b3, x-requested-with, x-stainless-lang, origin, referer, accept-language`。

### 2.4 `/v2/plugin/auth/token/refresh`（11 头，idx 556）

```
accept, authorization, content-type, user-agent, x-auth-refresh-source,
x-domain, x-product, x-refresh-token, x-request-id, x-requested-with, x-user-id
```
值：`accept=application/json, text/plain, */*`、`x-auth-refresh-source=plugin`、`x-product=SaaS`、UA 三段式。
**绝不应出现**：`origin, referer, accept-language, x-codebuddy-request`。
注：refresh **同时**带 `Authorization` 与 `X-Refresh-Token`（与 chat 相反，chat 永不带 X-Refresh-Token）。

### 2.5 `/v2/billing/meter/checkin-activity-status`（7 头，idx 314，23 条同构）

```
accept, authorization, content-type, user-agent, x-domain, x-request-id, x-user-id
```
值：`accept=application/json`、`content-type=application/json`、**`user-agent=axios/1.16.1`**（billing 层是 axios 实例，与客户端 UA 不同）。
**绝不应出现**：`origin, referer, accept-language, x-codebuddy-request`。

### 2.6 `/v3/config`（8 头，idx 558，9 条同构）

```
accept, authorization, user-agent, x-domain, x-product, x-request-id,
x-requested-with, x-user-id
```
值：`accept=application/json, text/plain, */*`、`x-requested-with=XMLHttpRequest`、`x-product=SaaS`、UA 三段式。
**绝不应出现**：`origin, referer, accept-language, x-codebuddy-request`。

### 2.7 逐头实现要点

- **`X-Private-Data` 取值逻辑**（`headers.go:482-487 @47f1ed6`）：客户端反编译为 `p[PRIVATE_DATA_HEADER] = resolveModelOptimizationEnabled(...) ? "false" : "true"`。网关没有"用户手动关优化"开关可读，故按账号形态二选一：

```go
func privateDataHeaderValue(a *auth.Auth) string {
	if a != nil && a.IsEnterprise() {  // 企业版默认关模型优化（合规）
		return "true"
	}
	return "false"                      // 个人号默认开优化 → false
}
```
  企业/个人的判据是 `auth.Auth.IsEnterprise()`（= `EnterpriseID != ""`），**不是** `token_source`。
- **`X-Agent-Runtime`**：`const agentRuntimeLocal = "local-agent"`（`headers.go:651 @47f1ed6`），在 `ChatHeaders` 末尾 `req.Header.Set("X-Agent-Runtime", agentRuntimeLocal)`（`headers.go:469`）。值域枚举 `["local-agent","cloud-agent","agent-agent"]`。
- **`baggage` 六键格式**（`headers.go:664-687 @47f1ed6`）：

```go
req.Header.Set("baggage", "buddyId=,buddyName=,codebuddy.session_id="+conv+
	",codebuddy.conversation_request_id="+reqID+
	",x-agent-runtime="+req.Header.Get("X-Agent-Runtime")+
	",x-agent-intent="+req.Header.Get("X-Agent-Intent"))
```
  空值保留 `k=` 形态（OTel W3CBaggagePropagator 序列化行为）；`codebuddy.*` 两键必须与同请求的 `X-Conversation-ID` / `X-Conversation-Request-ID` **同值**（头体一致是硬要求）。
- **`X-Machine-ID` / `X-Session-ID` 删除**：`CommonHeaders` 不再调用 `injectAccountStableHeaders`（`@47f1ed6` 里该函数**零调用方**、仅剩定义在 `headers.go:227`）。`ChatHeaders` 里的 `Del("X-Machine-ID")/Del("X-Session-ID")` 保留作兜底（`headers.go:398-399`）。设备标识改由 `/v2/report` **body** 的 `machineId`/`sessionId` 承载。
- **`Origin` / `Referer` 移除**：`CommonHeaders` 里两行删除（原 `@5367e78` 的 headers.go:209-211 位置），并在注释里写明「桌面端 RPC 层是 node fetch，无浏览器同源语义；需要浏览器语义的登录页/web 层自行设置 `originRefererFor`」。`originRefererCN` 同步改 `https://www.workbuddy.cn`（661 条带 Origin 的请求里没有一条用 codebuddy.cn）。
- **`Accept-Language` 整层移除**：`CommonHeaders` 与 `BillingHeaders` 两处调用都删掉；`acceptLanguageFor` 函数**保留但零调用**（`headers.go:279` 起，其上方 264-278 行是新加的"出站已不再调用本函数"注释）。
- **`X-CodeBuddy-Request` 只注入 chat**：`CommonHeaders` 删掉该行，改在 `ChatHeaders` 末尾 `c.injectCodeBuddyRequest(req)`（`headers.go:465 @47f1ed6`，8ecf115 引入）。依据：真机 7/7 条 chat 带、30+ 条业务路径一条没有。
- **chat 的 `X-Domain` 改为恒发**（`headers.go:434 @47f1ed6`，8ecf115 引入）：

```go
if a != nil && !a.IsGlobal() {
	if a.EnterpriseID != "" { req.Header.Set("X-Enterprise-Id", a.EnterpriseID) }
	// 不再用 a.DomainValue()，恒取 chat base 的裸主机名
	req.Header.Set("X-Domain", c.chatHost(a))
}
```
  `chatHost` = `internal/upstream/headers.go:763` 的 `hostOf(c.chatBase(a), "www.workbuddy.cn")`；`hostOf`（`headers.go:774`）剥 scheme/路径/端口，空则回落 fallback。
- **`BillingHeaders`**（`headers.go:790-830 @47f1ed6`）：新增 `X-Request-ID: session.NewTraceID()`（纯随机 32 hex）；删 `injectCodeBuddyRequest` + `Accept-Language`；`X-Domain` 改 `if d := a.DomainValue(); d != "" {d} else {c.billingHost(a)}`。
- **`RefreshHeaders`**（`headers.go:835-880 @47f1ed6`）：`Accept` 覆盖为宽松值、`X-Product: SaaS`、`Authorization: Bearer <accessToken>`（空则 `X-No-Authorization: "true"`）、`X-Request-ID: session.NewTraceID()`、`X-User-Id: a.UID`、`X-Domain`（domain 优先，否则 `chatHost`）。

---

## 3. body / 编码

### chat body gzip

```go
// client.go:937 @47f1ed6
const gzipMinChatBody = 1024

// client.go:941 @47f1ed6 —— BestSpeed（客户端用 zlib 默认级别；上游只解码不校验压缩率）
func gzipBody(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	...
}
```

调用点 `ChatStreamContext`（`client.go:1510-1527 @47f1ed6`）：

```go
outBody := prepared
gzipped := false
if len(outBody) >= gzipMinChatBody {
	if z, zerr := gzipBody(outBody); zerr == nil {
		outBody, gzipped = z, true
	}
}
req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(outBody))
...
if gzipped { req.Header.Set("Content-Encoding", "gzip") }
```

判据 = `len(prepared) >= 1024`（压缩后失败则静默发明文），级别的选择在 `gzipBody`，阈值常量在 `client.go:937`。
真机侧证据：body 以 `1f 8b` 开头、带 `Content-Encoding: gzip`，`Accept-Encoding: gzip, compress, deflate, br`（响应方向不变，本仓 `headers.go:276-277` 的"刻意不设 Accept-Encoding"与该改动不冲突）。

### refresh body

```go
// client.go:1357 @47f1ed6（doRefresh，函数起于 :1351）
req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
```
+ `RefreshHeaders` 里 `Accept: application/json, text/plain, */*`（`headers.go:838`）。真机 `Content-Length: 2` 而非 0。

### billing 无 body POST

```go
// report.go:41-45 @47f1ed6（billingJSON）
} else if method == http.MethodPost {
	// 真机 billing 族 POST（checkin-activity-status 等）Content-Length 恒 2、body 是 {}
	rdr = strings.NewReader("{}")
}
```

---

## 4. ID 形态

### `session.NewTimeOrderedID`（UUIDv7，`internal/session/ids.go:92 @47f1ed6`）

```go
func NewTimeOrderedID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40); b[1] = byte(ms >> 32); b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16); b[4] = byte(ms >> 8);  b[5] = byte(ms)
	if _, err := cryptorand.Read(b[6:]); err != nil {
		for i := 6; i < 16; i++ { b[i] = byte(ms >> uint((i%6)*8)) } // 熵源故障兜底，不 panic
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
```
取证注释：真机 `01a1815d-dacf-75f1-8919-7f596025b9b3` 前 48 bit 解出 1793171774159 ms、第 13 位恒 7。

### `buildTimeOrderedUUID`（`internal/upstream/cache_key.go:142 @47f1ed6`）

```go
func buildTimeOrderedUUID(uid, conversation, purpose string) string {
	in := uid + "|" + conversation
	if purpose != "" { in += "|" + purpose }
	sum := sha256.Sum256([]byte(in))
	const (
		anchorMS = int64(1767225600000)                 // 2026-01-01T00:00:00Z
		windowMS = int64(24 * 30 * 24 * 60 * 60 * 1000) // ≈24 个月
	)
	ms := uint64(anchorMS + int64(binary.BigEndian.Uint64(sum[8:16])%uint64(windowMS)))
	var u [16]byte
	u[0] = byte(ms >> 40); ... ; u[5] = byte(ms)
	copy(u[6:], sum[:10])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
```
要点（函数注释原文）：时间位不能取 `time.Now()`，否则同 (账号,会话) 每轮换值、前缀缓存槽全废；故把 48 bit 时间位也做成**哈希派生的伪时间**，锚 2026-01-01 + 24 个月窗口，落点是"合理的历史时刻"而非越界假值。

### 用 v7 / 用 v4 的分界

| 头 / 字段 | 形态 | 实现 |
|---|---|---|
| `X-Conversation-ID`（派生命中） | v7 | `buildTimeOrderedUUID(uid, src, "")`（headers.go:532 @47f1ed6） |
| `X-Conversation-ID`（零值兜底） | v7 | `session.NewTimeOrderedID()` |
| `acp-connection-id`（派生命中） | v7 | `buildTimeOrderedUUID(uid, src, "acp")` |
| `acp-connection-id`（零值兜底） | v7 | `session.NewTimeOrderedID()` |
| `prompt_cache_key` | **v4 不变** | `buildCacheKey` → `buildStableUUID`（刻意不改，避免存量会话缓存槽整体漂移一次） |
| `X-Request-ID` / `X-Conversation-Message-ID` / `X-Trace-ID` / billing `X-Request-ID` | 32 hex | `session.NewMessageID()` / `NewTraceID()` |

`X-Root-Request-ID` 仍是 32 hex（由 handler 用 `session.RequestIDForKey("root:"+sessKey)` 派生，见 §5）。

---

## 5. 可选头条件化

- **`X-Id-Source`**（`headers.go:310-345 @47f1ed6`）：`auth.IdSource()` 读 JWT 的 `id_source` 声明（新增函数，`internal/auth/auth.go:271-279 @47f1ed6`，复用已有 `jwtClaim`）。触发：`idSource != ""` 就发该头；`X-Userinfo` 里也带 `id_source` 段。

```go
// headers.go:316-336 @47f1ed6
enterprise := strings.TrimSpace(a.EnterpriseID)
method := a.TokenSource()
idSource := a.IdSource()
if idSource != "" { req.Header.Set("X-Id-Source", idSource) }
if enterprise == "" && idSource == "" { return }      // 个人号且无身份来源：三头全不发
if enterprise != "" && method != "" { req.Header.Set("X-Auth-Method", method) }
payload := map[string]any{"uin": a.UID}
if enterprise != "" { payload["owner_uin"] = enterprise }
if idSource != ""   { payload["id_source"] = idSource }
if enterprise != "" && method != "" { payload["token_source"] = method } // token_source 仅企业态
```
  关键修正：`token_source` **只在企业态下**才进 `X-Userinfo`（个人号的 token_source 是"来路"不是身份），而旧实现是 `method != ""` 就发 `X-Auth-Method`。
- **`X-Userinfo` 的 `id_source` 段**：见上，条件拼接（缺维度就不出现该键，不写空串）。
- **`X-Root-Request-ID` 会话级根**：`ChatMeta.RootRequestID`（`headers.go:370-376 @47f1ed6`）；`injectConversationHeaders` 空则回落轮级 `convReqID`：

```go
rootReqID := meta.RootRequestID
if rootReqID == "" { rootReqID = convReqID }
req.Header.Set("X-Root-Request-ID", rootReqID)
```
  触发点在 handler：`if sessKey != "" { chatMeta.RootRequestID = session.RequestIDForKey("root:" + sessKey) }`（`internal/server/handler.go @47f1ed6` 的 `chatCompletions`）。
- **`X-Agent-Type`（main / auxiliary）**：`ChatMeta.AgentType`（空 = `main`），常量 `AgentTypeMain` / `AgentTypeAuxiliary`（`headers.go:625-627`），`injectClientSDKHeaders(req, agentType)` 多一个参数（`headers.go:632`）。上游改用的三个触发点：`internal/panel/autotask.go` 的 `runModelChat`（面板判据对话）、`internal/upstream/blackcat.go` 的 `RunNightChats`（夜猫子）、`internal/server/searchtool.go` 的 `probeOnce`（web_search 探测）。真机另有 `compaction/subagent/team` 三档，网关不臆造。

---

## 6. billing 域切换

- `BillingBaseCN` 默认值：`internal/upstream/client.go:923 @47f1ed6` → `"https://www.workbuddy.cn"`（旧 `@5367e78` 为 `codebuddy.cn`）。
- 依据（提交 8ecf115 message 逐条列了条数）：`checkin-activity-status` 23 条、`get-user-resource-*` 12 条、`/activity/growth/*` 13 条、`/billing/ide/trial` + `/billing/pay/*` 5 条 —— host 全部 `www.workbuddy.cn`、全部 200，没有一条打 codebuddy.cn。
- 新增配置 `cn.billing_base`：

```go
// cmd/server/config.go:195 @47f1ed6（CN 段）
BillingBase string `json:"billing_base"`   // 空 = 内置默认 https://www.workbuddy.cn
```
```go
// cmd/server/main.go:909-916 @47f1ed6（applyCNChatBase，装配期一次性写入）
if cfg.CN.BillingBase != "" { up.BillingBaseCN = cfg.CN.BillingBase }
```
- **热生效方式：不热改**。与 `cn.chat_base` 同规格——装配期写入 Client 字段，请求路径并发只读；改它会被列入 `restartRequiredFields`（`cmd/server/main.go:944 @47f1ed6` 的 `changed("cn.billing_base", ...)`）。

---

## 7. `/v2/report`

三处改动（`ee1c80d`）：

1. **通道**：`billingBase` → **`chatBase`**（`report.go:296 @47f1ed6`：`http.NewRequest(http.MethodPost, c.chatBase(a)+reportPath, ...)`），且不再走 `billingJSON`/`BillingHeaders`，改为手构头族：

```go
// report.go:299-315 @47f1ed6
req.Header.Set("Accept", "application/json, text/plain, */*")
req.Header.Set("Content-Type", "application/json;charset=UTF-8")
req.Header.Set("X-Requested-With", "XMLHttpRequest")
req.Header.Set("X-Request-ID", session.NewMessageID())
req.Header.Set("X-Product", "SaaS")
req.Header.Set("User-Agent", desktopUAFor(a))
if at := a.AccessTokenValue(); at != "" { req.Header.Set("Authorization", "Bearer "+at) }
if a.UID != "" { req.Header.Set("X-User-Id", a.UID) }
req.Header.Set("X-Domain", c.chatHost(a))
// 真机 /v2/report 不带 X-Device-Token（只有 billing 族带）
```
2. **字段集**（`reportChatEventMap`，`report.go:196-230 @47f1ed6`）：`desktopFingerprint(a)` 打底（含 `ideName/ideType/ideVersion/commit/releaseDate/machineId/sessionId/extName/extVersion/os/arch/osVersion/cpuModel/cpuCores/memorySize/qimei36/userId/username/userNickname/product/timezone`），再 merge：
   - **删掉**：`maxToken, maxSteps, temperature, maxRetries, skillCount, totalCount, fileUri, expertId`；也删了 `timestamp / reportDelay / presentAt / userId`(从结构体里，userId 由 fingerprint 提供)。
   - **新增**：`isMaxMode, source, inputAttemptId, vcsType, vcsRepo, vcsBranchName, vcsRevId`，以及 `buddyId=""`、`buddyName=""`、`codebuddy.session_id`、`codebuddy.conversation_request_id`。
   - VCS 段恒 `vcsType="unknown"` + 三个空串。
3. **取值**：`agentName: "cli"`、`agentType: "main"`（旧 `"default"` / `"conversation"`）；`inputAttemptId = session.NewConversationID()`；`traceId = session.NewTraceID()`（不再留空）；`isMaxMode: true`、`source: "work"`；默认模型 `deepseek-v4.1-flash` / `DeepSeek-V4.1-Flash`（旧 `deepseek-v4-flash`）。
4. **附带**：`desktop.go:187 @47f1ed6` 给 `ReportDesktopEvent` 补 `X-Requested-With: XMLHttpRequest`（真机 /v2/report 必带）。

---

## 8. 本仓落地差异（哪些上游改法不能照搬）

> 前提事实：本仓 `internal/upstream/headers.go` 当前仍是 5.8.3 世代，但**已有上游没有的能力**：`b3`/`traceparent` 双发（headers.go:442-443）、chat 的 36 字符会话 ID 兜底（headers.go:416）、`X-API-Key` 已移除（本仓无静态 API Key 形态）。且 `headers.go` 里 `injectIdentityHeaders` 定义在文件尾部（:340 起），上游在 `ChatHeaders` 之前——**按函数名定位，不按行号移植**。

### 8.1 锚点清单（本仓）

| 位置 | 现状 | 对应上游改动 |
|---|---|---|
| `internal/upstream/headers.go:25` | `defaultClientVersionCN = "5.8.3"` | → 5.9.3 |
| `internal/upstream/headers.go:29` | `defaultCliVersion = "2.161.0"` | → 2.165.0（两域共用） |
| `internal/upstream/headers.go:31` | `originRefererCN = "https://www.codebuddy.cn"` | → workbuddy.cn（但见 8.2-①） |
| `internal/upstream/headers.go:203-222` `CommonHeaders` | 发 Origin/Referer(:209-211)、X-CodeBuddy-Request(:214)、Accept-Language(:217)、`injectAccountStableHeaders`(:221) | 四个都删，UA 保留 |
| `internal/upstream/headers.go:187-200` | `deriveAccountStableID` / `injectAccountStableHeaders` | 本仓可整段删除（上游只留定义不调用） |
| `internal/upstream/headers.go:261-322` `ChatHeaders` | Del 四头(:269-273)、X-Domain 仅当 DomainValue 非空(:300-302)、`X-Private-Data: "true"`(:310)、`injectClientSDKHeaders(req)`(:321) | 见 8.2-②/③/④/⑤ |
| `internal/upstream/headers.go:340-...` `injectIdentityHeaders` | `enterprise == "" → return`（:345-347），`method != ""` 即发 X-Auth-Method | 整函数重写（§5） |
| `internal/upstream/headers.go:473-483` `injectClientSDKHeaders(req)` | 单参，`X-Agent-Type` 恒 main | 加 `agentType` 参数 |
| `internal/upstream/headers.go:446-450` | `acp-connection-id` 走 `buildStableUUID`（v4） | → `buildTimeOrderedUUID`（v7） |
| `internal/upstream/client.go:848` / `:852` | ChatBaseCN workbuddy.cn / **BillingBaseCN codebuddy.cn** | billing 默认值切换（§6） |
| `internal/upstream/client.go:1142` | refresh `body = nil` | → `strings.NewReader("{}")` |
| `internal/upstream/client.go:1243-1290` `ChatStreamContext` | 循环 `chatPaths(a)` 里明文 JSON 出站（:1255 起） | gzip 阈值 + `Content-Encoding`（§3） |
| `internal/upstream/client.go:1891-1897` `fetchV3ConfigModelMap` | 有 `injectCodeBuddyRequest`，无 `X-Request-ID` | 掉前者、加后者 |
| `internal/upstream/report.go:1-42,102-206` | billingBase + `chatRequestEvent` 旧字段集 + `billingJSON` | 整段重构（§7） |
| `internal/upstream/desktop.go:50-52,75-79,147-190` | 5.8.3 三元组；`ReportDesktopEvent` 无 X-Requested-With | §1 + §7-4 |
| `internal/session/ids.go`（无 v7） | 只有 `NewMessageID:50` / `NewConversationID:64` / `NewTraceID:79` / `RequestIDForKey:113` | 追加 `NewTimeOrderedID` |
| `internal/upstream/cache_key.go:95` | 只有 `buildStableUUID`（v4；`buildCacheKey` 在 :83），文件 115 行 | 追加 `buildTimeOrderedUUID` |
| `internal/auth/auth.go:111,236,255` | 有 `TokenSource`/`IsGlobal`/`IsEnterprise`，**无 `IdSource`** | 追加 `IdSource()` |
| `internal/server/handler.go:1129-1145` | `chatMeta` 只填 ConversationID/ConversationRequestID/TraceID | 加 RootRequestID（`sessKey` 在 :954） |
| `internal/panel/autotask.go:810`、`internal/upstream/blackcat.go:54` | `ChatMeta{}` | 加 `AgentType: AgentTypeAuxiliary`（另需 `grep 'ChatStream('` 全量确认，本仓 `server/searchtool.go` 未走 ChatStreamContext） |
| `cmd/server/config.go:122-129` | CN 段只有 `chat_base`(:128)，**无 `billing_base`** | 加字段 + `applyCNChatBase`(main.go:419) + `restartRequiredFields`(main.go:647-651) |

### 8.2 不能照搬的六处 + 建议做法

- **① `originRefererCN` 改值 / `CommonHeaders` 删 Origin+Referer**：本仓 `originRefererFor` 唯一调用方是 `CommonHeaders`（`headers.go:209`），删掉后常量直接变死代码；而 `cmd/login/main.go:42` 有**另一份同名常量** `originRefererCN = codebuddy.cn`，那是 OAuth 设备登录（`copilot.tencent.com`）真正需要的浏览器 Origin，**绝不能跟着改**。建议：`CommonHeaders` 删两行；`internal/upstream` 侧常量改 workbuddy.cn 并注明"仅 web 层用"，或直接删常量与 `originRefererFor`（本仓无 web 层调用方）；`cmd/login` 一字不动。
- **② `X-Private-Data` 用 `IsEnterprise()` 分支**：可以照搬——本仓 `auth.IsEnterprise()` 已存在（auth.go:255），但本仓 `fingerprint_risk_test.go:48-50` 断言 `X-Private-Data == "true"`，必须同批改成「个人号 false / 企业号 true」。
- **③ chat 的 `X-Domain` 改成恒发 `chatHost`**：上游丢弃了 `a.DomainValue()` 优先。本仓老 auth 文件里 `domain` 可能是 `www.codebuddy.cn`（历史落盘值），照搬会把该值静默改写成 chat base host —— 这**大概率是想要的**（真机恒 `www.workbuddy.cn`），但属于行为变更，建议在 PR 说明里显式写明「auth.domain 不再影响 chat 的 X-Domain」。
- **④ `BillingHeaders` 加 `X-Request-ID` 会外溢到 growth 域**：本仓 `travel.go:59`、`tasks.go:99`、`tasks.go:120` 复用 `BillingHeaders`，但它们打的是 **chatBase 的 `/activity/growth/*`**，上游黄金集没有这几条路径的证据。建议：不要直接往 `BillingHeaders` 里塞，改为新增 `growthHeaders`（或在 `BillingHeaders` 加一个 `family` 参数），只对真正的 billing 族（`billingJSON`/`trial`/`school`）加 `X-Request-ID`。
- **⑤ billing 默认域切 workbuddy.cn 的外溢**：本仓除 `Client.BillingBaseCN` 外还有 `cmd/credit/main.go:29` 独立硬编码 `billingBaseCN = "https://www.codebuddy.cn"`（`cmd/credit` 是独立命令，按 codebuddy.cn 查积分）。改 Client 默认值不会影响它，但**两处口径会不一致**。需决策：`cmd/credit` 是否同批切。
  另：本仓 `client_test.go:534` 断言 `billingBase(cn) == "https://www.codebuddy.cn"`，`device_token_test.go:27` 用 `/v2/report` 当 billing 路径样本 —— 两处都要改。
- **⑥ `/v2/report` 改走 chatBase + 自建头族**：本仓 `report.go:204` 通过 `billingJSON`（`report.go:27-42`）发请求，改后 `billingJSON` 的「无 body POST 补 `{}`」只影响 checkin/trial/blackcat 等，方向正确；但注意本仓 `desktop.go:147 ReportDesktopEvent` **已经**走 `chatBase + desktopUAFor`，两条上报路径会重复同一套头族 → 建议抽一个 `reportHeaders(req, a)` 共用，避免两处再次漂移。

### 8.3 本仓额外注意

- 本仓 `ChatHeaders` 的 `X-No-Authorization` 哨兵值是 `"1"`（headers.go:284），上游 `RefreshHeaders` 用 `"true"`（headers.go:853 @47f1ed6）。移植 refresh 时按上游值写没问题（不同路径），但别把 chat 的 `"1"` 一起改掉。
- 本仓 `chatPaths` 的 global 侧有 `/console/chat/completions` → `/v2/chat/completions` 回退（client.go:890-905、:1220），上游 `cnGoldenCases` 只覆盖 CN 单路径。写黄金集时必须**只为 CN 建集**，或对 global 单独建一份。
- 本仓 `prepareBody` 签名是 `(body, realm, uid, conversationID, convReqID)`（client.go:945），上游是 `(body, realm, uid, conversationID)`，这条与本批改动无关，但迁移 gzip 时别顺手对齐。

---

## 9. 测试资产结构

### `internal/upstream/cn_contract_test.go`（8ecf115 新增，291 行）

- 数据结构：`cnGolden{path string; headers []string; values map[string]string; absent []string}`，`cnGoldenCases` 六条（§2 的六个路径）。
- 断言方式：`httptest.NewServer` 起一个假上游，用**生产函数**真实发六条请求（`ChatStream` / `ReportChatActivity` / `ModelBusy` / `RefreshToken` / `GetCheckinActivityStatus` / `fetchV3ConfigModelMap`），在 server 侧 `r.Header.Clone()` 抓头；
  - 集合比对：`got`（剔除 `transportOnly` 五类 + `content-encoding`）与 `want` **精确相等**，`extra` / `missing` 分别报错；
  - 值比对：`values` 逐键 `hdr.Get(k) != v`；
  - 反向断言：`absent` 逐键断言 `hdr.Get(k) == ""`。
- `transportOnly`（`cn_contract_test.go:155-162`）：`host, connection, content-length, accept-encoding, x-device-token, content-encoding`。
- 第二个测试 `TestCNIDShapes`：正则 `uuidV7Re = ^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`、`hex32`，钉零值 `X-Conversation-ID`/`acp-connection-id` 为 v7、`X-Request-ID` 为 32 hex、消息级两头同值、`X-Trace-ID != X-Conversation-Request-ID`。

### `internal/upstream/headers_parity_test.go`（47f1ed6 新增，204 行）

七个用例，钉「形态与条件分支」而非键集：
1. `TestDerivedConversationIDsAreV7` —— 派生分支必须 v7、跨请求稳定、与会话 ID 不同值；
2. `TestBuildTimeOrderedUUID` —— 形态/稳定/跨会话/跨 purpose/跨账号互异 + 解时间位落在 `[1767225600000, +24个月]`；
3. `TestIdentityHeadersIdSource` —— `X-Id-Source` + `X-Userinfo.id_source`，且个人号**不得**有 `owner_uin` / `token_source`；
4. `TestRootRequestIDSessionScoped` —— 根 ≠ 轮级；无根回落轮级；
5. `TestAgentTypePerScene` —— 缺省 main / 显式 auxiliary；
6. `TestPrivateDataTrueBranch` —— 企业 true / 个人 false；
7. `TestBaggageCarriesRuntimeKeys` —— baggage 含 `codebuddy.session_id=…`、`codebuddy.conversation_request_id=…`、`x-agent-runtime=<同名头值>`、`x-agent-intent=<同名头值>`。

### 本仓重写这些断言时的差异

- **本仓无 `/console` 回退路径**（CN 侧单路径）——但 global 侧有，故 golden case 需按 realm 分开建，或只建 CN。
- **本仓无静态 API Key 账号形态**（`X-API-Key` 已删），上游 `ChatHeaders` 里那段 `IsAPIKey()` 分支不必搬，测试也不必建 API Key 账号。
- 需同步修改的本仓既有断言：
  - `internal/upstream/fingerprint_risk_test.go:48-50`（`X-Private-Data == "true"`）→ 改按账号形态；同文件 `:37-40` 的 absent 列表仍成立（chat 侧确实没有 X-Machine-ID/X-Session-ID），但改动后 `X-CodeBuddy-Request` 变成 chat 必带，`TestChatHeadersStripBrowserSemantics` 里若把它列入 absent 需调整（当前未列）。
  - `internal/upstream/headers_conversation_test.go:43`（断言 `X-Root-Request-ID == X-Conversation-Request-ID`）→ 必须反转。
  - `internal/upstream/client_test.go:534`（`billingBase(cn) == codebuddy.cn`）→ 新默认值。
  - 本仓**没有** `acp-connection-id` 的形态断言（`headers_conversation_test.go:97` 只断言"存在"），也没有上游的 `TestACPConnectionIDShape` / `TestChatMetaCacheKeySource` —— 改 v7 时要**新增**形态断言（`uuidV7Re`）而不是改旧断言。
  - gzip 上线后，凡是 `io.ReadAll(r.Body)` 后直接 `json.Unmarshal` 的测试，遇到 **≥1024 字节**的 body 会读到 gzip 字节（小 body 因阈值不压缩，不受影响）：本仓 `client_test.go:339` 一类用的都是 `{"model":...,"messages":[]}` 这种几十字节 body，安全；但 `sanitize_test.go:272/315/357`（长 system 文本）、`wire_integration_test.go:49`（真实请求体）大概率越阈值，需加解压。稳妥做法：把 `gzipMinChatBody` 做成 `Client` 字段（默认 1024），测试里设为极大值即等价于关闭压缩。

---

## 10. 本仓移植清单

### 必须改（照搬或等价重写，风险低）

1. `headers.go:25` `defaultClientVersionCN` → `"5.9.3"`；`headers.go:29` `defaultCliVersion` → `"2.165.0"`。
2. `desktop.go:50-52` CN 三元组 → `5.9.3 / d46924219e79332f49c37ccebf27292f87001b19 / 1793093056315`。
3. `CommonHeaders` 删 `Origin`/`Referer`/`X-CodeBuddy-Request`/`Accept-Language`/`injectAccountStableHeaders` 五行；`originRefererCN` 改 `https://www.workbuddy.cn`（`cmd/login` 的同名常量不动）。
4. `ChatHeaders` 末尾补 `injectCodeBuddyRequest(req)`、`X-Agent-Runtime: local-agent`、`injectBaggage(...)`；`X-Private-Data` 改 `privateDataHeaderValue(a)`。
5. `injectConversationHeaders`：`X-Conversation-ID` 派生/兜底、`acp-connection-id` 派生/兜底全部改 v7；`X-Root-Request-ID` 支持 `meta.RootRequestID` 回落。
6. `ChatMeta` 加 `RootRequestID`、`AgentType`；`injectClientSDKHeaders` 加 `agentType` 参数；新增 `AgentTypeMain/Auxiliary` 常量。
7. `injectIdentityHeaders` 重写为 id_source 条件触发（含 `X-Userinfo` 三段条件拼接）；`auth.IdSource()` 新增。
8. `session.NewTimeOrderedID` + `buildTimeOrderedUUID`（§4 原文）。
9. `fetchV3ConfigModelMap`：删 `injectCodeBuddyRequest`、加 `X-Request-ID`。
10. `doRefresh` body → `{}`；`RefreshHeaders` 补 `Accept`/`X-Product`/`Authorization`/`X-Request-ID`/`X-User-Id`/`X-Domain`。
11. `report.go` 整段重构（chatBase + 黄金头族 + 新字段集 + `agentName=cli`/`agentType=main` + `deepseek-v4.1-flash`）。
12. `billingJSON` 无 body POST 补 `{}`。
13. `ChatStreamContext` 出站 body gzip（阈值 1024 / BestSpeed / 条件设 `Content-Encoding`）。
14. `desktop.go ReportDesktopEvent` 补 `X-Requested-With: XMLHttpRequest`。
15. `handler.chatCompletions` 填 `chatMeta.RootRequestID = session.RequestIDForKey("root:"+sessKey)`。
16. `panel/autotask.go:810`、`upstream/blackcat.go:54`（+ 全量 grep `ChatStream(`）改 `AgentType: AgentTypeAuxiliary`。
17. 新增 `cn_contract_test.go`（CN 六条黄金集）+ `headers_parity_test.go`（七组形态断言），并按 §9 改四处既有断言。

### 需决策

1. **`BillingBaseCN` 切 workbuddy.cn**：会影响 checkin/trial/school/balance/report 全族；`cmd/credit/main.go:29` 是独立口径，是否同批切？旧口径可用 `cn.billing_base` 退回——是否同时加这个配置项（本仓 CN 段当前只有 `chat_base`）。
2. **chat 的 `X-Domain` 丢弃 `auth.DomainValue()`**：老 auth 文件里的 `domain`（可能是 codebuddy.cn）将被静默改写为 chat host。
3. **`X-Request-ID` 是否进 `BillingHeaders`**：会外溢到 `travel.go`/`tasks.go`（growth 域，无抓包证据）；建议拆 growth 头族。
4. **`/v2/chat/busy` 轻头族**：本仓**没有** `ModelBusy` 客户端函数（`grep 'chat/busy'` 空），是「本批不涉及」还是「顺手补上」，需定。
5. **gzip 阈值是否可配**：`gzipMinChatBody` 做成常量还是 Client 字段（影响多处 httptest 断言）。
6. **`X-Userinfo` 的 `token_source` 收紧到仅企业态**（上游）与本仓 `enterprise=="" → 全不发` 是否合并成一致语义 —— 上游新逻辑下"个人号但带 id_source"会发 `X-Userinfo{uin,id_source}`，本仓现状是全不发。

### 不要照搬

1. `cmd/login/main.go:42` 的 `originRefererCN` —— OAuth 设备登录（copilot.tencent.com）真的需要 codebuddy.cn 作 Origin。
2. 上游 `ChatHeaders` 里的 `X-API-Key` / `IsAPIKey()` 分支 —— 本仓已刻意移除，无该账号形态。
3. 上游 `injectAccountStableHeaders`（保留定义零调用）—— 本仓直接删掉 `deriveAccountStableID` 与 `injectAccountStableHeaders` 更干净。
4. `acceptLanguageFor` 保留函数不删——本仓无「测试钉两域同值」的包袱，若确认无调用可直接删（需先 grep `Accept-Language` 相关测试）。
5. `desktopUAFor` 用 `defaultCliVersion` 绕过 `c.cliVersion()` —— 这是上游的一个瑕疵（config `upstream.cli_version` 对 report 路径失效）；本仓若要保持配置一致性，建议改用 `c.cliVersion()`，但要在注释里标明与上游差异。
6. CN 的 `v3ProbeUALite`/双路探测——本仓的探测策略与上游不同（`client.go:1440` 注释：两路并发、并集去重），不要按上游注释里的"19/32/54 条"改本仓探测分支；只改 UA 里的版本段。
