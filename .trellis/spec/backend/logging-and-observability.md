---
name: backend-logging-and-observability
description: 每请求一行表格日志、JSONL 请求归档、脱敏规则、用量账本口径
paths:
  - internal/server/logging.go
  - internal/reqlog/**
  - internal/logfmt/**
  - internal/usage/**
---

# 日志与可观测性

三层观测，各有明确边界：

| 层 | 产物 | 有没有正文/提示词 |
|----|------|:---:|
| 控制台流水行 | 每请求一行表格 | 没有 |
| 请求归档 `reqlog` | JSONL，一天一卷，可轮转保留 | 没有 |
| 用量账本 `usage` | `usage.json` 聚合桶 | 没有 |

原则：**排障要的是"哪一跳、哪个号、多快、多少 token"，不是"用户问了什么"**。提示词、响应正文、`Authorization`、原始 API key 一律不落任何一层。

## 每请求一行（`internal/server/logging.go`）

只在请求结束时打印一行，**不按尝试打**：

```
| #007 | 15:04:05 | model(26) | mode | status | 昵称(uid8) | TTFB=123ms | tok=456 | 12.3tok/s | total=4.5s | rid=req-… | out=success | try=2 | credit=0.1234 | src=1.2.3.4 ua="OpenAI/Python" |
```

- `#seq` 是进程级自增（`chatSeq atomic.Int64`），列宽固定：模型 26、账号 22、TTFB/tok/速率各有宽度常量。
- `tok` 缺失时显示 `-`（内部用 `toks<0` 表示 usage 缺失），**不做 rune/字节估算**——token 一律采信上游 usage。
- 速率必须走与用量账本同一个 `tokensPerSecond`（要扣掉 TTFB）。历史上这里自己除 `total`，导致流水行与面板数字对不上（issue #34）。
- 账号列是 `logfmt.Label(uid, nick)` = `昵称(uid8)`，**只补不截**：宁可整行变宽也不丢昵称，昵称是排障主线索。
- 扩展段（`rid/out/try/credit`）仅在 `requestID != ""` 时追加；来源段（`src/ua`）仅在真的采集到 IP/UA 时追加，都不采集则保持旧行格式。

打印闸门：`chatLogEnabled` + 输出目标 `chatLogOut`。包级 `TestMain` 会关掉它，断言 stdout 的测试用 `withChatLog` 临时打开。`SetChatLogOutput` 是唯一注入点（`cmd/server` 用它把日志同时写到面板环形缓冲）。

## 脱敏（强制）

| 敏感物 | 处理 | 实现 |
|--------|------|------|
| uid | 只显示前 8 位 | `logfmt.UID8` / `logfmt.Label` |
| API key | `abcd******wxyz` | `apikeys.Redact` |
| 客户端 UA | 截断到 200 字节 + 压缩成客户端标签 | `maxUserAgentLen`、`logfmt.ShortUA` |
| 上游请求体/响应片段 | 截断到 200 字节 | `logfmt.Truncate` |
| 客户端 IP / UA | **默认不记录**，只有 `livecfg.RecordClientInfo` 为真才采集 | `captureClientInfo` |
| 提示词 / 响应正文 / Authorization | 永不记录 | — |

`internal/reqlog` 的 `Event` 结构体里**没有** body/message 字段——这是刻意的结构级约束，不要为了"方便排障"加一个 `body` 字段。

## 请求归档（`internal/reqlog`）

- 一行一个 JSON 事件，字段固定：`time / request_id / path / account / model / status / ok / outcome / duration_ms / ttfb_ms / attempts / prompt_tokens / completion_tokens / total_tokens / credit / credit_known / cache_hit_tokens / cache_miss_tokens / client_ip / user_agent`。
- `X-Request-Id` 形如 `req-<hex>`，**只在 `RequestLog != nil` 时分配**，同时进响应头与归档。
- `outcome` 取值含 `success` / `http_error` / `stream_error` / `interrupted` 等；空值由 `event()` 根据 status 推导。`ok` = `2xx && outcome==success`。
- 保留策略来自配置：`logging.request_archive_enabled` / `request_retention_days` / `request_archive_max_mb`，这三个都是**重启生效**字段。
- 写入是异步队列（`QueueSize`、`DroppedWrites` 计数），满了丢写而不是阻塞请求。
- 空查询结果必须返回 `[]` 而不是 `null`（面板把 `null` 当"归档未启用"）。

## 用量账本（`internal/usage`）

- 每次**尝试**都计数，失败也计（否则重试放大在用量视图里看不见）。入口 `Add(now, realm, uid, model, delta, ok)`。
- 桶键 `scope|realm|uid|model|rate`，`scope` 是 `h:2006-01-02T15`（小时）或 `d:2006-01-02`（天）。
- 口径规则：
  - `total` 缺失时用 `prompt+completion`。
  - `credits_per_1m_tokens` 的分子分母只统计**同一次请求里同时有 credit 与 tokens** 的样本。
  - 缓存命中率只在分母 > 0 时输出。
  - 平均值是"按样本加权求和 / 样本数"，不是均值的均值。
  - 无样本的字段在面板显示 `—`，不显示 `0`。
- 保留：小时桶留 90 天，更早的 `Rollup` 折成天桶（先加后删，幂等）；桶数硬上限 40 万，触顶立即 rollup。
- 落盘 `data/usage.json`：30s 去抖 + `tmp`+`Rename` + `0o600`；文件 `version: 4`，**schema 只增不改**（旧版本缺字段按零值加载）。
- 面板查询：**一个时间窗统管所有维度**（totals / by_realm / by_account / by_model / series）。历史上"卡片=全量、曲线=时间窗"的分裂被用户当成"筛选没生效"，已移除。
- 序列输出顺序：先所有天点升序，再所有小时点升序。

## 其它观测面

- `/status`：池快照 + 计数，**只增键不改语义**。
- `/healthz`：`X-Service` + body `service` 字段，供负载均衡/宿主探活；判定口径必须与 chat 一致（模型级豁免也要计入）。
- 面板环形缓冲：进程内日志快照，由 `cmd/server` 用 `io.MultiWriter(os.Stderr, pn.Logs())` 接入。
- 缓存低命中告警：同一模型此前命中过、本次大前缀（≥1000 tok）整段未命中（<10%）才打一条 WARN，每模型 10 分钟冷却——只报"本可命中却重算"的可行动信号。

## 反模式

- 用 `fmt.Printf` / `log.Printf` 直接打提示词、上游 body 原文、完整 uid。
- 请求归档里加正文字段；或把 `Event` 当调试日志用。
- 面板自己在 Go 里重算用量聚合（必须走 `usage.SnapshotWindow`）。
- 新增一层日志却不接 `SetChatLogOutput`，导致面板看不到。
- 在流水行里自己算 tok/s（必须复用 `tokensPerSecond`）。
