// Package headers 构造三类上游请求头（common / chat / billing / refresh）。
// 规则来自 docs/api-reference.md §0/§4/§6。
package upstream

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
)

const (
	// defaultClientVersionCN / defaultClientVersionGlobal 出站 WorkBuddy 客户端版本段
	// （UA 的 `<brand>/<ver>` 与白名单头组的 X-IDE-Version）。
	//
	// 取值来源：官方两个分发包的 install-manifest —— 国内版 5.9.3（www.workbuddy.cn，
	// 2026-10-28 上游 47f1ed6 真机复核）、国际版 5.8.4（www.workbuddy.ai，无新版抓包
	// 证据，**不臆造**）。两版发版节奏不同，故**按 realm 分开**而不是共用一值：
	// 国际版号用 CN 的 5.9.3 会与上游实际分发包对不上。
	// config upstream.client_version 可覆盖（空 = 按 realm 取内置默认）。
	defaultClientVersionCN     = "5.9.3"
	defaultClientVersionGlobal = "5.8.4"
	// defaultCliVersion 出站 UA 中 `CLI/<ver>` 段版本。对齐官方内置 CLI（2.165.0，
	// 两版分发包内置同一版本）。config upstream.cli_version 可覆盖。
	//
	// 注意这是**两域共用单常量**：跟着 CN 从 2.161.0 升到 2.165.0 时 global UA 的
	// CLI 段也被动跟着变，这是本批唯一波及 global 的改动（CN 5.9.3 分发包内置
	// CLI 已升级，没有 5.8.4 配套的新 CLI 抓包证据）。
	defaultCliVersion = "2.165.0"
)

// 出站 Origin/Referer 基础域常量已删除（2026-10-28，上游 47f1ed6）：桌面端 RPC 层是
// node fetch，没有浏览器同源语义，chat/refresh/billing 逐条真机抓包都没有这两个头。
// 需要浏览器语义的调用方自己设（`internal/panel/login.go` 与 `cmd/login` 各有一份
// 独立的 originRefererCN，那才是 OAuth 设备登录真正需要的 codebuddy.cn）。

// clientVersion 生效的 WorkBuddy 客户端版本：Client.ClientVersion 非空则取之，
// 否则按账号 realm 取内置默认（CN 5.9.3 / global 5.8.4）。
func (c *Client) clientVersion(a *auth.Auth) string {
	if c != nil && c.ClientVersion != "" {
		return c.ClientVersion
	}
	if a != nil && a.IsGlobal() {
		return defaultClientVersionGlobal
	}
	return defaultClientVersionCN
}

// uaBrandFor 出站 UA 的**第二段**品牌：按 realm 取，两版形态不同（不同分发包）。
//
// 真机双版本抓包（同一台机器、同一套代理）：
//   - 国内版 5.9.3（productName `WorkBuddy`）→ `WorkBuddy/5.9.3`，**驼峰原样**；
//   - 国际版 5.8.4（productName `WorkBuddy AI`）→ `workbuddy-ai/5.8.4`，**小写连字符
//     slug**（Electron 的 userAgentFallback 替换路径会把非字母数字换成 `-` 并转小写）。
//
// 反例警示：曾把 CN 也写成小写 `workbuddy`（按 GL 规则统一），与实测不符——CN 的每个
// 业务请求都是驼峰，写成 slug 反而是最明显的非官方指纹。
func uaBrandFor(a *auth.Auth) string {
	if a != nil && a.IsGlobal() {
		return "workbuddy-ai"
	}
	return "WorkBuddy"
}

// defaultWorkBuddyUAFor 组装默认客户端出站 UA（官方桌面端 RestOperations 层形态）：
// `WorkBuddy/<ver> <brand>/<ver> CLI/<cli>`。
//
// 首段**恒为驼峰 `WorkBuddy`**（两版实测都如此），只有第二段品牌随 realm 变；
// 版本段按 realm 取（CN 5.9.3 / global 5.8.4）。官方 UA 无任何随机化，故此处确定性拼装。
func (c *Client) defaultWorkBuddyUAFor(a *auth.Auth) string {
	ver := c.clientVersion(a)
	return "WorkBuddy/" + ver + " " + uaBrandFor(a) + "/" + ver + " CLI/" + c.cliVersion()
}

// cliVersion 生效的 CLI 版本：Client.CliVersion 非空则取之，否则内置默认 defaultCliVersion。
func (c *Client) cliVersion() string {
	if c != nil && c.CliVersion != "" {
		return c.CliVersion
	}
	return defaultCliVersion
}

// defaultWorkBuddyUA 返回 CN 形态的默认 UA（默认账号形态即 CN，零回归兼容既有调用/测试）。
func (c *Client) defaultWorkBuddyUA() string {
	return c.defaultWorkBuddyUAFor(nil)
}

// v3ProbeUA /v3/config 目录探测的官方 UA（真机实测形态）。
//
// 2026-10-20 双版本抓包实测，两版对同一个 /v3/config 端点发的 UA 并不同：
//
//	CN 5.9.3     → WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0   （三段，重复品牌）
//	global 5.8.4 → WorkBuddy/5.8.4 CLI/2.165.0                    （两段，单品牌）
//
// 旧实现在这里发 `CodeBuddyIDE/4.12.0 CodeBuddy/4.12.0` 与
// `CLI/2.63.2 CodeBuddy/2.63.2` 两套 CodeBuddy 家族 UA —— 那是从旧版 IDE
// 客户端抄来的，WorkBuddy 分发包里根本不存在这两个串，属于最显眼的非官方指纹。
func (c *Client) v3ProbeUA(a *auth.Auth) string {
	ver := c.clientVersion(a)
	if a != nil && a.IsGlobal() {
		return "WorkBuddy/" + ver + " CLI/" + c.cliVersion()
	}
	return "WorkBuddy/" + ver + " " + uaBrandFor(a) + "/" + ver + " CLI/" + c.cliVersion()
}

// v3ProbeUALite /v3/config 第二路探测 UA：单品牌两段式。
//
// 保留双路探测是因为该端点按 UA 家族下发**不同模型集合**，只探一族会让 /v1/models
// 少模型。但两路都必须是官方真实出现过的形态——这里取 global 的 config UA 形态
// （`WorkBuddy/<ver> CLI/<cli>`，与 v3ProbeUA 在 CN 下的三段式互补）。
//
// 注意：global 账号下本函数与 v3ProbeUA 返回同一串（真机 global 只发这一种形态），
// 于是两路探测退化为重复请求——无害（并集去重），但不要据此认为 global 有第三族。
func (c *Client) v3ProbeUALite(a *auth.Auth) string {
	return "WorkBuddy/" + c.clientVersion(a) + " CLI/" + c.cliVersion()
}

// userAgent 返回当前出站 UA（客户端出站路径：chat/refresh/FetchModels）。
// 优先级：Client.UserAgent（config user_agent）显式覆盖 > 按账号 realm 的默认 WorkBuddy 三段式。
// 显式覆盖兼容既有覆盖逻辑：用户配了即以用户值为准（自定义品牌/版本），
// 未配则走官方桌面端默认形态（global 换 `WorkBuddy AI` 平台段）。
func (c *Client) userAgent(a *auth.Auth) string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	return c.defaultWorkBuddyUAFor(a)
}

// billingUA 白名单类（billing/checkin/banner）出站 UA：**恒为 axios 默认串**。
//
// 双版本真机抓包实测：两版 billing 层（summary / paid-packages / free-packages /
// checkin-activity-status / ambassador status）发的都是 `axios/1.16.1`，与客户端自身
// UA 完全不同——billing 域的 RestOperations 走 axios 实例而非 SDK 客户端，所以不带
// 任何品牌段。此前发 `WorkBuddy/<ver>` 是「按客户端 UA 推理」的产物，实测不符。
//
// 显式 client_name="SaaS" 时仍不设 UA（保留既有逃生开关，还原 Go 默认 UA）。
func (c *Client) billingUA(a *auth.Auth) string {
	if c == nil || c.attributionClientName() == "SaaS" {
		return ""
	}
	return "axios/1.16.1"
}

// resolveDeviceToken 解析本次请求的 X-Device-Token 取值。
// 优先级：auth.Auth.DeviceToken（每号）> Client.DeviceToken（config 全局）> 文件兜底。
// 三者皆空/读失败则返回空串（调用方不注入该头，优雅降级）。
func (c *Client) resolveDeviceToken(a *auth.Auth) string {
	if a != nil && a.DeviceToken != "" {
		return a.DeviceToken
	}
	if c != nil && c.DeviceToken != "" {
		return c.DeviceToken
	}
	if c != nil && c.DeviceTokenFile != "" {
		return readDeviceTokenFile(c.DeviceTokenFile)
	}
	return ""
}

// injectDeviceToken 在 req 注入 X-Device-Token 头（仅当取到非空 token）。
func (c *Client) injectDeviceToken(req *http.Request, a *auth.Auth) {
	if tok := c.resolveDeviceToken(a); tok != "" {
		req.Header.Set("X-Device-Token", tok)
	}
}

// 已删除的死代码（2026-10-28，上游 47f1ed6 / ee1c80d）：
//   - deriveAccountStableID + injectAccountStableHeaders（X-Machine-ID / X-Session-ID）
//   - acceptLanguageFor（Accept-Language 整层移除）
//
// 三头都是「网关自造、客户端从不发」的形态：5.9.3 CN 逐条真机抓包（chat 38 键黄金集）
// 里 machineId/sessionId 走的是 /v2/report 的 **body**，头族一条都没有；Accept-Language
// 桌面端 RPC 层根本不发。上游保留了这三个函数的定义但零调用，本仓连定义一起删——
// 留着会被下一轮同步当成"能力"再注回去（多一个客户端不发的头本身就是指纹）。

// CommonHeaders 设置所有 API 共享的请求头：**只发 RPC 层共有的最小集**。
//
// 2026-10-28 按 5.9.3 真机黄金头集双向收敛（上游 47f1ed6，chat/report/refresh/
// v3-config/billing 五条路径逐头比对）：删掉客户端从不发的 Origin / Referer /
// X-CodeBuddy-Request / Accept-Language / X-Machine-ID / X-Session-ID。
// 其中 X-CodeBuddy-Request 改为**只在 chat 路径**注入（见 ChatHeaders）——真机
// 7/7 条 chat 带、30+ 条业务路径一条没有。
func (c *Client) CommonHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	// Accept 非流式默认 application/json（D6：去掉宽松的 text/plain, */*）。
	// chat 流式路径在 ChatHeaders 覆盖为 event-stream。
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", c.userAgent(a))
}

// injectCodeBuddyRequest 在 req 注入 X-CodeBuddy-Request: 1。
// 只给 chat 路径用（真机 chat 恒带，业务路径一条不带），故不放进 CommonHeaders。
func (c *Client) injectCodeBuddyRequest(req *http.Request) {
	req.Header.Set("X-CodeBuddy-Request", "1")
}

// ChatMeta 一次 chat 出站的会话头族元数据（issue #35：后台按 X-Conversation-Request-ID
// 聚合请求，官方客户端一次 user send 内所有 tool call/重试/换号复用同一个 ID）。
// handler 在轮转循环外生成 conversationID / conversationRequestID，循环内每次出站
// 复用同值；TraceID 透传入站值（空 = 回落 conversationRequestID）。
// messageID（消息级，每条独立）由 ChatHeaders 内部生成，无需外部可见。
type ChatMeta struct {
	ConversationID        string // X-Conversation-ID：body 提取的入站值
	ConversationRequestID string // X-Conversation-Request-ID：轮级聚合主键，必发
	TraceID               string // X-Trace-ID：入站透传值，空则独立新值（不回落 request id）
	// RootRequestID 会话级根请求 ID（X-Root-Request-ID）：handler 用
	// session.RequestIDForKey("root:"+会话键) 派生，整段会话内恒定；空则回落轮级
	// ConversationRequestID（无会话概念的调用方的既有形态）。
	RootRequestID string
	// AgentType X-Agent-Type 取值：空 = main（既有行为），辅助链（面板判据对话 /
	// 夜猫子）传 AgentTypeAuxiliary。真机另有 compaction/subagent/team 三档，网关
	// 不臆造。
	AgentType string
	// CacheKeyID 网关派生的粘性会话键（客户端未声明会话时的会话头族来源）。
	// 与 prompt_cache_key 同源，保证「同一客户端会话在不同账号上」各用独立缓存槽。
	CacheKeyID string
}

// ChatHeaders 在 common 之上加 chat 专属的账号头。
// 缺省字段用 X-No-* 约定（与 CodeBuddy 官方 CLI 一致）。
// clientIP 为本次请求的客户端 IP（按参数传递，不读共享字段——避免并发串扰）；
// PassthroughIP=false 或 clientIP 为空时不注入 IP 头。
// meta 为会话头族元数据，见 injectConversationHeaders。
func (c *Client) ChatHeaders(req *http.Request, a *auth.Auth, clientIP string, meta ChatMeta) {
	c.CommonHeaders(req, a)
	// Accept 与官方 chat 完全一致：客户端只发 application/json，照样收 SSE 流
	// （5.9.3 与 5.8.4 实测；旧值多带 ", text/event-stream" 属自造）。
	// Accept-Encoding 刻意不设：官方发 br/gzip 全集，但显式设置会关闭 net/http 的
	// 透明解压，收到 br 时网关没有 brotli 解码器 —— 收益（一个头）远小于风险。
	req.Header.Set("Accept", "application/json")
	// AccessToken 加锁快照：keepalive 定时刷新会在 a.mu 内改写它，锁外直读构成数据竞争
	// （见 auth.AccessTokenValue 注释）。
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
	// 安全红线：绝不在 chat 请求里携带 X-Refresh-Token。
	// 企业/域头：**只发有值的**。桌面端个人号实测两个方向都不发 X-No-* 哨兵头
	// （capture/raw3 国际版、capture/raw8 国内版，逐头对照均无 X-No-Enterprise-Id /
	// X-No-Department-Info）。旧实现用 X-No-* 声明「无企业」，那是 CodeBuddy CLI 的
	// 约定，不是 WorkBuddy 桌面端的形态——多发一个客户端从不发的头同样是指纹。
	if a != nil && !a.IsGlobal() {
		if a.EnterpriseID != "" {
			req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		}
		// X-Domain 恒发 chat base 的裸主机名：真机 661 条带 Origin 的请求全是
		// www.workbuddy.cn，`codebuddy.cn` 只是 billing API 主机名。由此 auth 文件里
		// 历史落盘的 domain 字段**对 chat 不再生效**（行为变更，5.9.3 同步刻意为之）。
		req.Header.Set("X-Domain", c.chatHost(a))
	} else {
		c.injectGlobalChatHeaders(req, a)
	}
	// 用量归属头：默认伪造 WorkBuddy 桌面端指纹（client_name="SaaS" 还原旧行为）。
	c.injectAttribution(req, a)
	// X-Private-Data 按账号形态（见 privateDataHeaderValue）：企业版默认关模型优化
	// → "true"，个人号默认开 → "false"。旧实现写死 "true"，与 5.9.3 真机抓包相反。
	req.Header.Set("X-Private-Data", privateDataHeaderValue(a))
	// 身份头族（X-Auth-Method / X-Userinfo）：按账号形态条件注入，见
	// injectIdentityHeaders——个人号三头全不发才是官方形态。
	c.injectIdentityHeaders(req, a)
	// 客户端 IP 透传（仅 PassthroughIP=true 且本次请求带 IP）。
	c.injectClientIP(req, clientIP)
	// 设备风控头：auth 每号 > config 全局 > 文件兜底；空则不注入。
	c.injectDeviceToken(req, a)
	// 会话头族（对话/请求/消息/B3 链路），见 injectConversationHeaders。
	c.injectConversationHeaders(req, a.UID, meta)
	// SDK / agent 段指纹头（x-stainless-* / x-agent-type / x-agent-intent）。
	c.injectClientSDKHeaders(req, meta.AgentType)
	// 以下三头必须在 SDK 段之后：baggage 的 x-agent-runtime / x-agent-intent 两键
	// 要与同名**头**同值，故先从头上取值再序列化。
	c.injectCodeBuddyRequest(req)
	req.Header.Set("X-Agent-Runtime", agentRuntimeLocal)
	c.injectBaggage(req)
}

// privateDataHeaderValue X-Private-Data 的取值（5.9.3 真机反编译
// `resolveModelOptimizationEnabled(...) ? "false" : "true"`）。
//
// 网关没有"用户手动关模型优化"的开关可读，故按账号形态二选一：企业版默认关模型
// 优化（合规）→ true；个人号默认开 → false。判据是 EnterpriseID，**不是**
// token_source——后者描述 token 的来路，与隐私设置无关。
func privateDataHeaderValue(a *auth.Auth) string {
	if a != nil && a.IsEnterprise() {
		return "true"
	}
	return "false"
}

// agentRuntimeLocal X-Agent-Runtime 取值。真机值域为
// ["local-agent","cloud-agent","agent-agent"]；网关是本地进程，恒 local-agent。
const agentRuntimeLocal = "local-agent"

// injectBaggage 注入 OTel baggage 头（6 键）。
//
// 硬要求：`codebuddy.session_id` / `codebuddy.conversation_request_id` 必须与同请求的
// X-Conversation-ID / X-Conversation-Request-ID **同值**（头体一致）。因此这里直接
// 从已注入的头读回，而不是再传一份参数——多一个参数就多一处可能漂移。
// `buddyId=` / `buddyName=` 空值也保留 `k=` 形态（OTel W3CBaggagePropagator 的
// 序列化行为，删掉空键反而不是客户端形态）。
func (c *Client) injectBaggage(req *http.Request) {
	req.Header.Set("baggage",
		"buddyId=,buddyName="+
			",codebuddy.session_id="+req.Header.Get("X-Conversation-ID")+
			",codebuddy.conversation_request_id="+req.Header.Get("X-Conversation-Request-ID")+
			",x-agent-runtime="+req.Header.Get("X-Agent-Runtime")+
			",x-agent-intent="+req.Header.Get("X-Agent-Intent"))
}

// injectIdentityHeaders 注入官方客户端的身份头族（X-Auth-Method / X-Userinfo）。
//
// 两个头都是**按账号形态条件发送**，不是常量。5.9.3 真机反编译的等价逻辑（上游
// 47f1ed6）：
//
//	enterprise := EnterpriseID
//	method     := token_source（JWT 声明）
//	idSource   := id_source（JWT 声明）
//	idSource != ""                → X-Id-Source = idSource
//	enterprise == "" && idSource == "" → 三个身份头全不发（纯个人号）
//	enterprise != "" && method != ""   → X-Auth-Method = method
//	X-Userinfo = base64(JSON{uin, owner_uin?, id_source?, token_source?})
//
// 三条判据各自独立：
//   - `X-Id-Source` 只看 id_source（新增维度，个人号也可能带）；
//   - `X-Auth-Method` 只看企业态 + token_source；
//   - `token_source` **只在企业态下**进 X-Userinfo——个人号的 token_source 是 token
//     的来路（实测 CN 设备流恒为 "enterprise_switch"）不是账号身份，旧实现据此给
//     个人号注入了身份头，与真机相反。
//
// X-Userinfo 的段落是**条件拼接**：缺哪个维度就不出现该键，而不是写空串。
func (c *Client) injectIdentityHeaders(req *http.Request, a *auth.Auth) {
	if a == nil || a.UID == "" {
		return
	}
	enterprise := strings.TrimSpace(a.EnterpriseID)
	method := a.TokenSource()
	idSource := a.IdSource()
	if idSource != "" {
		req.Header.Set("X-Id-Source", idSource)
	}
	if enterprise == "" && idSource == "" {
		return // 个人号且无身份来源：与官方客户端一致，三头全不发
	}
	if enterprise != "" && method != "" {
		req.Header.Set("X-Auth-Method", method)
	}
	payload := map[string]any{"uin": a.UID}
	if enterprise != "" {
		payload["owner_uin"] = enterprise
	}
	if idSource != "" {
		payload["id_source"] = idSource
	}
	if enterprise != "" && method != "" {
		payload["token_source"] = method
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req.Header.Set("X-Userinfo", base64.StdEncoding.EncodeToString(raw))
}

// injectGlobalChatHeaders global 账号的 chat 专属域声明头。
//
// 只发 X-Domain：桌面端个人号实测并不发 X-No-Enterprise-Id（旧实现按 CodeBuddy CLI
// 约定补的哨兵头），国际版 5.8.4 与国内版 5.8.3 的真机报文里都不存在该头
// （capture/raw3/001-chat.json、capture/raw8/002-chat.json）。
func (c *Client) injectGlobalChatHeaders(req *http.Request, a *auth.Auth) {
	if a == nil || !a.IsGlobal() {
		return
	}
	req.Header.Set("X-Domain", "www.workbuddy.ai")
}

// injectConversationHeaders 注入官方客户端会话头族（issue #35 后台聚合）。
//
// **三件套齐全**是渠道校验的一部分：上游要求 X-Conversation-ID /
// X-Conversation-Request-ID / X-Conversation-Message-ID 同时在场，缺任一头即
// HTTP 400 code=11-128「conversation id missing」（官方 chat/speech 都带）。
// 因此 X-Conversation-ID 不再「空则不发」——客户端没声明就按会话键补，连会话概念
// 都没有的调用方（夜猫子对话/面板任务）现造一个，绝不留给上游缺失形态。
//
// 头族分层：
//   - X-Conversation-ID：会话级，多轮稳定。客户端声明值优先 → 会话键（与
//     prompt_cache_key 同源，含账号隔离段）→ 现造 UUID v7。
//   - X-Conversation-Request-ID：**对话轮级聚合主键**，必发。一次 user send 内的
//     所有 tool call/重试/换号/降级复用同一个 → 后台按它聚合成一条。
//   - X-Conversation-Message-ID = X-Request-ID：消息级，每条独立（32 位 hex）。
//   - X-Root-Request-ID：会话级根（ChatMeta.RootRequestID，handler 按会话键派生）；
//     空则回落轮级 conversationRequestID。
//   - X-Trace-ID：入站透传，否则独立新值（**不**回落 conversationRequestID——
//     官方实测两族恒不相等）。
//   - X-B3-TraceId / SpanId / ParentSpanId / Sampled：链路族，TraceId 跟随
//     X-Trace-ID；SpanId/ParentSpanId 必须独立随机（拿 messageID 切片会带出时间位）。
//   - b3 / traceparent：官方两版都双发（缺它们的头集合在形态上可区分）。
//   - acp-connection-id：官方列进标准出站头族，按「同 (账号, 会话) 稳定、与会话 ID
//     不同值」派生；形态与 X-Conversation-ID 同为 UUID v7（5.9.3 真机
//     `01a1815d-dacf-75f1-...` 前 48 bit 可解出毫秒时间、第 13 位恒 7）。
func (c *Client) injectConversationHeaders(req *http.Request, uid string, meta ChatMeta) {
	convReqID := meta.ConversationRequestID
	if convReqID == "" {
		// 零值 meta（直接调 ChatHeaders 的调用方/测试）也要保证聚合主键必发：
		// 本级补一个 32 hex，调用方（handler）已生成稳定的不走到这里。
		convReqID = session.NewMessageID()
	}
	messageID := session.NewMessageID()
	src := cacheKeySource(meta)
	if meta.ConversationID != "" {
		req.Header.Set("X-Conversation-ID", meta.ConversationID)
	} else if src != "" {
		// 客户端没声明会话：用与 prompt_cache_key 同源的 uid 隔离键填该头。
		// 不带 uid 段的话，同一客户端会话打在不同账号上会共用同一个上游缓存槽。
		// 形态是 v7（时间位取哈希伪时间，**不**取 time.Now()——否则同 (账号,会话)
		// 每轮换值，缓存槽全废）；prompt_cache_key 自身仍是 v4（见 buildCacheKey）。
		req.Header.Set("X-Conversation-ID", buildTimeOrderedUUID(uid, src, ""))
	} else {
		// 无会话概念的调用方：头族是渠道校验的一部分，缺任一头即 400 11-128，
		// 故现造一个（该路径无会话状态，不必跨请求稳定）。
		req.Header.Set("X-Conversation-ID", session.NewTimeOrderedID())
	}
	req.Header.Set("X-Conversation-Request-ID", convReqID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)
	// 根请求 ID：会话级（两者不同值——真机根请求 ID 是一次会话的锚点，轮级 ID
	// 是一轮 user send 的锚点）。无会话键的调用方回落轮级，保持既有形态。
	rootReqID := meta.RootRequestID
	if rootReqID == "" {
		rootReqID = convReqID
	}
	req.Header.Set("X-Root-Request-ID", rootReqID)
	// X-Trace-ID 与 request id **互不回落**：真机实测两族恒不相等，回落会让两族头
	// 恒等，成为「网关自造」的可见痕迹。入站透传值仍优先。
	traceID := meta.TraceID
	if traceID == "" {
		traceID = session.NewTraceID()
	}
	req.Header.Set("X-Trace-ID", traceID)
	// B3 跟着 trace 走；非法（含横线/超长/非 hex）时回落恒 32 hex 的 messageID。
	b3Trace := traceID
	if !validTraceID(b3Trace) {
		b3Trace = messageID
	}
	req.Header.Set("X-B3-TraceId", b3Trace)
	// SpanId / ParentSpanId 必须独立随机：官方 span 与 trace/request 均无关系。
	spanID := session.NewSpanID()
	parentSpanID := session.NewSpanID()
	req.Header.Set("X-B3-SpanId", spanID)
	req.Header.Set("X-B3-ParentSpanId", parentSpanID)
	req.Header.Set("X-B3-Sampled", "1")
	// 单头 B3 与 W3C traceparent：官方两版都双发。
	req.Header.Set("b3", b3Trace+"-"+spanID+"-1-"+parentSpanID)
	req.Header.Set("traceparent", "00-"+b3Trace+"-"+spanID+"-01")
	// acp-connection-id：同 (账号, 会话) 稳定、与会话 ID 不同值（官方两个值不同但
	// 都按会话稳定）。无会话源时现造一个。形态同为 v7。
	if src != "" {
		req.Header.Set("acp-connection-id", buildTimeOrderedUUID(uid, src, "acp"))
	} else {
		req.Header.Set("acp-connection-id", session.NewTimeOrderedID())
	}
}

// 官方 chat 出站的自报 SDK 指纹（国际版与国内版真机实测**完全一致**）。
//
// 这些是 openai-node SDK 自动加的 x-stainless-* 头 + 业务语义头 x-agent-type/intent。
// 网关是 Go 实现，没有对应的 SDK 运行时；这里按官方实测值固定，理由是「客户端形态
// 一致性」优先——不发这几个头，上游看到的是一份缺了 SDK 段的残缺头集合。
// 一旦客户端版本变化（package-version / node 版本），这几个常量需要跟着更新。
const (
	clientArch     = "x64"
	clientOS       = "Windows"
	clientLang     = "js"
	clientRuntime  = "node"
	clientNodeVer  = "v22.21.1"
	clientSDKVer   = "6.25.0"
	agentIntentCrf = "craft"
)

// X-Agent-Type 取值（真机值域另有 compaction / subagent / team 三档，网关不臆造）。
// 留空等价 AgentTypeMain——零值 ChatMeta 的既有行为不变。
const (
	AgentTypeMain      = "main"
	AgentTypeAuxiliary = "auxiliary"
)

// injectClientSDKHeaders 注入 x-stainless-* 与 x-agent-* 两族出站头。
// 仅在 chat 路径调用（ChatHeaders）——billing / checkin 等 REST 域没有真机对照证据，
// 不臆造。
//
// agentType 空 = main：辅助链（面板判据对话 / 夜猫子）显式传 AgentTypeAuxiliary，
// 让上游把网关自发的后台会话与用户真实对话区分开。
func (c *Client) injectClientSDKHeaders(req *http.Request, agentType string) {
	if agentType == "" {
		agentType = AgentTypeMain
	}
	req.Header.Set("X-Agent-Type", agentType)
	req.Header.Set("X-Agent-Intent", agentIntentCrf)
	req.Header.Set("X-Stainless-Lang", clientLang)
	req.Header.Set("X-Stainless-Package-Version", clientSDKVer)
	req.Header.Set("X-Stainless-OS", clientOS)
	req.Header.Set("X-Stainless-Arch", clientArch)
	req.Header.Set("X-Stainless-Runtime", clientRuntime)
	req.Header.Set("X-Stainless-Runtime-Version", clientNodeVer)
	req.Header.Set("X-Stainless-Retry-Count", "0")
}

// validTraceID 判断 B3 TraceId 是否合法：16 或 32 位 hex（大小写均可）。
// 官方客户端生成的 conversationRequestId 是 32 位 hex（UUID 去横线），入站透传值
// 可能是任意形状（含横线/超长/非 hex），直接塞进 B3 头会破坏链路关联（issue #35）。
func validTraceID(s string) bool {
	if len(s) != 16 && len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

// attributionClientName 生效的用量归属名：ClientName 非空取之；
// 空默认 "WorkBuddy"（伪造官方桌面端指纹；显式配 "SaaS" 可还原旧行为）。
func (c *Client) attributionClientName() string {
	if c != nil && c.ClientName != "" {
		return c.ClientName
	}
	return "WorkBuddy"
}

// injectAttribution 注入用量归属头（X-Agent-Purpose / X-IDE-* / X-Product）。
// 仅在 chat/completions 路径生效（ChatHeaders 调用）。
//
// X-Product 恒为 "SaaS"，与 X-IDE-* 分开取值——这是真机抓包纠正过的形态（issue #51）：
//
//	国际版 5.8.4 出站：X-Product: SaaS  X-IDE-Name: WorkBuddy  X-IDE-Type: WorkBuddy  X-IDE-Version: 5.8.4
//
// 旧实现让 X-Product 也取客户端名（=WorkBuddy），于是同一个头官方发 SaaS、网关发
// WorkBuddy——「同名不同值」比多一个头更容易被规则命中。X-IDE-* 才是承载产品名的地方。
func (c *Client) injectAttribution(req *http.Request, a *auth.Auth) {
	name := c.attributionClientName()
	// X-Product 是 SKU 段，恒 SaaS：官方两版实测都不是产品名。
	req.Header.Set("X-Product", "SaaS")
	if name == "SaaS" {
		// 显式 ClientName=SaaS：仅声明 SKU，不补 X-IDE-*（旧行为，保留兼容）。
		return
	}
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-IDE-Name", name)
	req.Header.Set("X-IDE-Type", name)
	req.Header.Set("X-IDE-Version", c.clientVersion(a))
}

// injectClientIP 在 PassthroughIP 开启时把 clientIP 参数透传给上游（三等价头）。
func (c *Client) injectClientIP(req *http.Request, clientIP string) {
	if c == nil || !c.PassthroughIP || clientIP == "" {
		return
	}
	req.Header.Set("X-Forwarded-For", clientIP)
	req.Header.Set("X-Real-IP", clientIP)
	req.Header.Set("X-Client-IP", clientIP)
}

// ExtractClientIP 从入站请求提取客户端 IP 首段（X-Forwarded-For 首段，回落 X-Real-IP）。
func ExtractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return strings.TrimSpace(xff[:i])
			}
		}
		return strings.TrimSpace(xff)
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return ""
}

// hostOf 从 base URL 提取裸主机名；解析失败回落 fallback。
// X-Domain 类头必须是**裸主机名**（真机 `www.workbuddy.cn`），带 scheme 会与其它头
// 不自洽，是最容易被按「非客户端形态」识别出来的一处。
func hostOf(base, fallback string) string {
	if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return fallback
}

// chatHost chat 出站 X-Domain 的取值：chat base 的裸主机名。
// 真机 chat 的 X-Domain 恒是 chat 域主机名（661 条带 Origin 的请求无一例外），
// 与账号落盘 domain 无关——这也是 chat 不再读 auth.DomainValue() 的原因。
func (c *Client) chatHost(a *auth.Auth) string {
	return hostOf(c.chatBase(a), "www.workbuddy.cn")
}

// billingHost billing 出站 X-Domain 的兜底取值：billing base 的裸主机名。
// 账号落盘 domain 非空时优先（见 BillingHeaders）——billing 域保留旧优先级，
// 只有 chat 丢掉了 domain 优先。
func (c *Client) billingHost(a *auth.Auth) string {
	return hostOf(c.billingBase(a), "www.workbuddy.cn")
}

// BillingHeaders billing 接口请求头。
// UA 语义：默认**不设置**（保持现状，Go 客户端自带默认 UA）；仅当显式配置
// c.UserAgent 非空才覆盖——避免默认路径给 billing 引入新的 UA 指纹。
//
// X-Request-ID **不在这里**：BillingHeaders 被 travel.go / tasks.go 复用于 chatBase
// 的 /activity/growth/*，那几条路径没有真机抓包证据，往共用头族里塞会外溢成新的
// 形态偏差。X-Request-ID 只在真正的 billing 族入口（report.go 的 billingJSON）注入。
func (c *Client) BillingHeaders(req *http.Request, a *auth.Auth) {
	// AccessToken 加锁快照（同 ChatHeaders：keepalive 可在 a.mu 内改写）。
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c != nil && c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	} else if ua := c.billingUA(a); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	// X-Domain：账号落盘 domain 优先，空则 billing base 主机名（5.9.3 真机形态）。
	if d := a.DomainValue(); d != "" {
		req.Header.Set("X-Domain", d)
	} else {
		req.Header.Set("X-Domain", c.billingHost(a))
	}
	// 设备风控头：billing 域（report/travel/balance/checkin）同样注入。
	c.injectDeviceToken(req, a)
}

// RefreshHeaders refresh 端点专属头（X-Refresh-Token 只允许出现在这里）。
//
// 5.9.3 真机 refresh 黄金头集（11 键，剔除 transportOnly）：accept /
// authorization / content-type / user-agent / x-auth-refresh-source / x-domain /
// x-product / x-refresh-token / x-request-id / x-requested-with / x-user-id。
// 与 chat 相反，refresh **同时**带 Authorization 与 X-Refresh-Token。
func (c *Client) RefreshHeaders(req *http.Request, a *auth.Auth) {
	c.CommonHeaders(req, a)
	// Accept 覆盖为宽松值（真机 refresh 是 `application/json, text/plain, */*`）。
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Product", "SaaS")
	// Authorization：access token 为空时发显式哨兵（真机 refresh 走插件链路，
	// 此时没有可用 AT）。注意值域与 chat 的 X-No-Authorization: "1" 不同——两条路径
	// 是不同客户端层，别把 chat 的 "1" 一起改。
	if at := a.AccessTokenValue(); at != "" {
		req.Header.Set("Authorization", "Bearer "+at)
	} else {
		req.Header.Set("X-No-Authorization", "true")
	}
	req.Header.Set("X-Request-ID", session.NewMessageID())
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if d := a.DomainValue(); d != "" {
		req.Header.Set("X-Domain", d)
	} else {
		req.Header.Set("X-Domain", c.chatHost(a))
	}
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	}
	// X-Auth-Refresh-Source 对齐官方客户端 refresh 渠道标识 "plugin"（D3）。
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
}
