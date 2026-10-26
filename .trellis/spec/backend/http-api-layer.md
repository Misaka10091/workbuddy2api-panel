---
name: backend-http-api-layer
description: OpenAI 兼容 HTTP 层：请求管线顺序、错误响应形状、状态码映射、流式契约、请求级状态
paths:
  - internal/server/**
---

# HTTP / API 层

`internal/server` 只做四件事：鉴权、**编排**（改写 + 轮换 + 闸门）、响应整形、观测。协议细节属于 `internal/upstream`，账号状态属于 `internal/pool`。

## 路由表（全部）

```go
// handler.go NewHandler
mux.HandleFunc("POST /v1/chat/completions", h.withAuth(h.chatCompletions))
mux.HandleFunc("GET  /v1/models",           h.withAuth(h.models))
mux.HandleFunc("GET  /status",              h.withAuth(h.status))
mux.HandleFunc("GET  /healthz",             h.healthz)          // 故意不鉴权
if cfg.Panel != nil { mux.Handle("/panel/", cfg.Panel) }        // 面板自带鉴权
```

- 用 Go 1.22 的 `"METHOD /path"` pattern；方法不匹配由 `ServeMux` 返回 405，不要自己判。
- `/healthz` 不鉴权但要带身份：`X-Service` 头 + body 里的 `service` 字段；其"可服务"判定必须和 chat 的选号判定同口径。
- 只有 `POST /v1/chat/completions` 会分配 `X-Request-Id` 并写指标/归档；`/v1/models`、`/status`、`/healthz` 不写。
- 没有 `/v1/completions`、`/v1/embeddings`、`/v1/responses`、CORS、`/metrics`。加之前先确认客户端真的需要。

## 请求管线（顺序是契约）

1. `ServeHTTP` → 生成 `X-Request-Id`（`reqlog.NewRequestID`）、可选采集客户端 IP/UA（受 `livecfg.RecordClientInfo` 控制）、`RequestLog.Begin`，defer 里用 `responseObserver` 记录**实际写出**的状态码。
2. `withAuth` → `authenticate`：先比主密钥（`httpauth.VerifyBearer`，常量时间），再查 `cfg.APIKeys.Lookup`。三种失败分别返回 `invalid_api_key` / `api_key_disabled`（401）。
3. `chatCompletions`：
   - `clientIP := upstream.ExtractClientIP(r)` —— **每次调用取，不放结构体字段**。
   - `io.ReadAll(r.Body)`，读失败 → 400 `invalid_request`。**不设请求体上限**（`max_body_mb` 已被有意移除）。
   - 轻量 peek（只取 `stream` / `model`），`json.Unmarshal` 错误故意忽略。
   - `declaredTools := upstream.ToolNameAllowlist(body)` —— **必须在任何改写之前**取，改写会动 `tools`。
   - `resolveModel(peek.Model)` 拆 realm 前缀（大小写敏感，前缀必须是精确小写 `cn:` / `global:`）。
   - key 的 realm 绑定：裸模型名继承 key 的 realm；显式相反前缀 → 400 `realm_mismatch`（不静默改路由）。
   - key 的模型白/黑名单按**裸模型名**判定，不允许 → 403 `model_not_allowed`。
   - 会话键：`session.ExtractKey` → `ResolveForModel`；然后 `session.TurnKey`。**TurnKey 必须在 `prompt.Rewrite` 之前取**，否则改写动 messages 会让键漂移。
4. 请求级前置：粘性账号租约（`Pool.Acquire` + defer `Release`）、`releaseHeld` / `unbindSticky` / `fail` / `recordAttempt` 闭包。
5. 提示词改写（server 层）：按 `PromptMode` 走 `prompt.Rewrite` / `prompt.Append`，降级中走 `prompt.Degraded`；随后**无条件** `prompt.EnsureSystemFirst`（上游要求 `messages[0]` 是 system）；必要时 `rewriteModel` 把 body 里的带前缀模型名换成裸名。
6. 会话头（`ChatMeta{ConversationID, ConversationRequestID, TraceID}`）在轮换循环**外**生成一次，循环内逐字节复用。
7. 轮换循环 `for i := 0; i < MaxRotate; i++`：粘性 `PickByUIDForModel` 优先，否则 `PickExcludingForRealm(tried, bareModel, realm)`；`Acquire` CAS 失败即换号；`NeedsRefresh` 时先 `RefreshToken`；然后 `ChatStreamContext(r.Context(), ...)`。
8. 失败分类（见 [error-handling.md](./error-handling.md)）→ 决定"换号 / 罚号 / fail-fast / 直接返回"。
9. 成功：`stream:true` 走 SSE 转发；否则 `upstream.Aggregate` 聚合成单响应。

成功判定与粘性绑定一律**延后到这一跳真正成功之后**：上游返回 200 但第一帧是 error 帧的情况曾经把限流账号记成健康。

## 错误响应形状

```go
writeJSON(w, status, v)                                  // 先 Content-Type 再 WriteHeader
writeOpenAIError(w, status, code, msg)                   // {"error":{"message","type":"api_error","code"}}
writeOpenAIErrorHint(w, status, code, msg, hint)         // 上面 + error.gateway_hint
writeModelQuotaError(w, quotaCode, ...)                  // {"code":6004,"msg":…,"error":{…,"code":6004}}
```

- `error.type` 恒为字面量 `"api_error"`（不用 OpenAI 的 `invalid_request_error` 家族）。
- `code` 是 snake_case 字符串；**模型整池限额**（6004/6000）例外，`code` 是上游数字码，且**不带 `Retry-After`**——客户端普遍把它当"睡多久"。
- `message` 在上游有 body 时**原样透传上游 body**，本地兜底文案只在上游 body 为空时使用。附加解释只走 `gateway_hint` 平行字段，**绝不替换或包装 message**。
- 本包实际会发出的状态码：200 / 400 / 401 / 403 / 429 / 502 / 503。500 不在这里产生。

## 流式与非流式

- 出站**永远**是流式：`upstream/payload.go` 强制 `stream=true` 并补 `stream_options.include_usage`。客户端的 `stream` 只决定它拿到什么。
- SSE 路径：先 `PeekFirstDataEvent` 看第一帧，若第一帧是 error 帧或上游空流 → veto 并换号；否则用 `readerWithCloser` 把已读字节接回，交 `upstream.StreamHint` 逐帧转发（`text/event-stream` + `no-cache` + `X-Accel-Buffering: no` + 每帧 flush）。
- 非流式路径：`upstream.Aggregate(rc)` → `writeJSON(w, 200, resp)`；聚合失败 → 502 `upstream_parse`。
- 工具标记还原由 server 层装配、upstream 执行：`upstream.NewMarkupRepair(declaredTools, true)`（流式）、`upstream.RepairAggregatedResponse(resp, declaredTools, true)`（非流式）。第二参数 `true` 表示白名单为空时走"弱判定"。**别在 server 里自己解析标记文本。**
- `reasoning_content` 回填完全在 upstream 侧，`internal/server` 里不该出现这个字段。`grep reasoning_content internal/server` 应为空。

## 请求级状态与并发

`Handler` 只持有进程级状态：`cfg`、`mux`、`degrade`、`wafIP`。其余全部是参数或局部变量/闭包（`tried`、`lastErr`、`heldUID`、`stickyUID`、`degradedApplied` …）。早年把 client IP 放在共享字段上造成过竞态，注释里保留了这个教训。

- 退出用**请求 context**：`ChatStreamContext(r.Context(), …)`；每个 `continue` 前走 `rotateBackoff(i, r.Context())`，取消即 `break`。
- 退避只有一份实现：`backoff.go` 的 `backoffAfter` / `jitterDur` / `sleepCtx`。禁止在别处再写一份 jitter。
- 请求级闸门都是**不续期**的一次性窗口：`degrade.go` 的 `degradeGate`（到 CST 零点）、`wafip.go` 的 `wafIPGate`（一个窗口，且只统计**不同号**，同一个号反复 403 永不触发）。新加同类闸门照此形状：`mu + 截止时间`，进程内，重启即清。

## 超时

- 服务端：`ReadHeaderTimeout` 30s、`ReadTimeout` 来自配置、`IdleTimeout` 120s，**不设全局 `WriteTimeout`**（会误杀在途 SSE）。
- 上游：首字节由 `Transport.ResponseHeaderTimeout` 管；流中停滞由 `IdleTimeout` + `monitorBody` 管。
- **上游超时/停滞**（`net.Error.Timeout()`、显式 deadline、或客户端仍在但 ctx 被空闲看门狗掐掉）视为基础设施故障：**不换号、不罚号**，直接 `break` 返回。换号撞的是同一个慢上游，只会把客户端拖到 `MaxRotate × header_timeout`。
- **普通传输错误**（连不上、连接被重置）不是同一回事：换号 + `NoteFailures`（连败达 `degrade_threshold` 才降权）。判据在 `isUpstreamTimeout`。

## 对外契约只增不改

- `/status` 的改动只能新增字段；既有字段语义不变（注释明确要求"零回归只增键"）。
- `/v1/models` 空列表也要 200，不允许编造模型目录。
- 面板 JSON 字段同理；空结果必须是 `[]` 而不是 `null`（前端把 `null` 当"功能未启用"）。

## 网关级闸门（2026-10 新增）

除密钥级白/黑名单外，`internal/server` 还有两道**网关级**闸门，二者都是"配置空/0 = 关闭"：

- **模型白名单**（`models.allowed`）：`/v1/models` 与 chat 入口取交集。条目语法 `裸名`（两域）
  或 `cn:xxx` / `global:xxx`（单域），大小写敏感。列表必须先在**带前缀的原始目录**上过滤再
  拆前缀——反过来单域视图里 id 已无前缀，无法区分 `cn:` 与 `global:`。拒绝复用 403
  `model_not_allowed`，文案不区分「不存在 / 未在白名单」，避免枚举探测。
- **积分闸**（`budget.*`，`budget.go`）：当日上限 / 5 分钟突发 / 连续低缓存率三道，在 chat
  入口 `admit()`，命中回 429 `insufficient_quota`（当日到顶）或带具体原因的快速闸文案。
  记账只认 `hasCredit=true` 的 `usage.credit`（缺失≠0），按 CST 自然日惰性归零；
  缓存率只在 `hasCache=true` 时判定。它是"止血"不是账本，进程内不落盘。

工具调用观测在 `chatStat` 与 `reqlog.Event`：`toolsDeclared` 取请求声明数，`toolCalls`
流式按 `delta.tool_calls[].index` 去重 + 标记还原数，非流式按 `message.tool_calls` +
还原数（**两种组包形态都要认**，否则非流式恒 0）。

## 反模式

- 在 handler 里 `Classify` 第二次：`ChatStreamContext` 已经分类过一次并带回 `Retry-After`。
- 把 error 帧塞进 SSE 白名单重建（会丢 `code/msg/requestId`）。
- 对请求级错误（参数非法、内容拦截、提示词过长）也换号或罚号。
- 在 handler 里读 `cfg.X` 拿热改字段，而不是 `loadLive()`。
- 给 `/healthz` 加鉴权，或让它与 chat 用不同的可服务判定。
