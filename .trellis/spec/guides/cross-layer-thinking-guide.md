---
name: guides-cross-layer
description: 请求/响应的跨层链路、边界自检清单、真实边界事故表
---

# 跨层思考指南

> 本仓库绝大多数线上事故发生在**层与层之间**，而不是某一层内部。这份指南给的是"改一个跨层字段前必须问的问题"，以及本项目真实踩过的边界事故。

---

## 1. 先画出这条链

一次对话请求的真实路径：

```
客户端
  → internal/server/handler.go  ServeHTTP → withAuth → chatCompletions
      · 取 declaredTools（改写前）
      · session.ExtractKey → ResolveForModel（粘性）
      · session.TurnKey（必须在 prompt.Rewrite 之前）
      · prompt.Rewrite / Append / EnsureSystemFirst
      · rewriteModel（body 里的模型名去前缀）
      · ChatMeta（conversation/trace id，轮换循环外生成一次）
  → 轮换循环
      · pool.PickByUIDForModel / PickExcludingForRealm
      · pool.Acquire（在途租约）
      · auth.NeedsRefresh → upstream.RefreshToken
  → internal/upstream
      · prepareBody（21 步改写，顺序固定）
      · headers.go（出站身份/会话三件套）
      · transport → /v2/chat/completions（永远是流式）
      · sse.go normalizeFrame / StreamHint / Aggregate
      · dsml.go 工具标记还原
  → 回程
      · 流式：StreamHint 逐帧 + error 帧原样透传
      · 非流式：Aggregate → RepairAggregatedResponse → writeJSON
      · 成功判定与粘性绑定：在这一跳真正成功**之后**
      · logging.go 一行流水 + usage.Add + reqlog.Event
```

反向链路（面板/排程）也跨同样几层：

```
scheduler → pool（状态机唯一的写入方）→ persist（本地文件 + Redis 镜像）
panel（Go 编排）→ pool / upstream / usage / reqlog（只读或既有方法）
cmd/server → 注入闭包把 upstream 的能力递给 pool（pool 不 import upstream）
```

---

## 2. 边界问题清单（逐条问）

改任何跨层字段/契约前，按顺序过一遍：

1. **字段名与序列化形态是否一致？**
   Go 的 `json` tag ↔ 面板 `app.js` 读的键 ↔ `usage.json` / 状态文件的历史键。改名 = 破坏历史数据。
2. **空值语义是什么？**
   `[]` 还是 `null`？面板把 `null` 当"功能未启用"。`nil` vs `[]` 在 `apikeys` 里还有"不改"与"清空"的区别。
3. **模型名带不带 realm 前缀？**
   进 handler 时可能带 `cn:`/`global:`，进 payload 第 1 步后是裸名，面板展示又带前缀。查表用裸名，展示用带前缀。
4. **这个值是"装配期快照"还是"每次读实时"？**
   装配期字段改了必须进 `restartRequiredFields`；热改字段必须走 `livecfg.Load()` 或既有 setter。
5. **错误类别跨层后还保持原意吗？**
   `upstream.Classify` 给出 Kind，handler 决定换号/罚号，pool 决定时长。任何一层自己重新判错误码文本都会分叉。
6. **这层能做这个决策吗？**
   upstream 不做换号；pool 不发网络；panel 不改池语义；scheduler 不直接改 `entry` 字段。
7. **计数/单位一致吗？**
   token 一律采信上游 usage；tok/s 必须扣 TTFB；`credit` 有"是否已知"标志；时间有 UTC / CST(+08) / 本地三种语境。
8. **失败在哪一层被"记账"？**
   一次尝试只记一次（usage），一次请求只打一行流水，罚号只发生在 pool 的原语里。

---

## 3. 真实边界事故（都可复核）

| 事故 | 根因 | 位置 |
|------|------|------|
| 限流账号被记成健康 | 上游回 200 但第一帧是 error 帧，成功判定发生在"拿到响应"而不是"这一跳真成功" | `handler.go` 轮换循环的 `recordAttempt` / 粘性绑定位置 |
| 会话键漂移 | `TurnKey` 在 `prompt.Rewrite` 之后才取，改写动了 messages | `handler.go` 的 `turnKey` 注释 |
| 面板显示"已保存"但没生效 | `upstream.user_agent` 被出站 client 在装配期捕获，当时既不在 livecfg 也不在重启清单（现已补入；同类缺口仍在，见 config 规范） | `cmd/server/main.go` `restartRequiredFields` |
| 单模型限流升级成整号冷却 | 带重置时间的 6004 被无条件写成账号级 `until`（现在只有无重置文案时才如此） | `internal/pool/cooldown.go` `CooldownSoftForModel` |
| 整池被推到 2h 封顶 | 冷却中重复失败还在推进 streak / 延长 `until` | `cooldown.go` / `degrade.go` 的"不叠加"注释 |
| 客户端拿不到上游错误详情 | error 帧被塞进 SSE 白名单重建，`code/msg/requestId` 被剥掉 | `sse.go` 的 `writeRaw` 分支 |
| 工具调用完全没净化 | 净化只扫 `content`，漏了 `tool_calls[].function.arguments` | `sanitize.go` |
| 新建的密钥删不掉 | 前端拿脱敏展示值当行主键 | `app.js` 的 `data-akrow` 约定 |
| 面板筛选"没生效" | 卡片用全历史、曲线用时间窗，口径分裂 | `internal/usage` 的"一个时间窗统管" |
| 深链页面永远读取失败 | 顶层 `go()` 早于模块级 `let/const` 初始化（TDZ） | `app.js` 末尾的 `setTimeout(…, 0)` |
| JS 白屏但 Go 测试全绿 | `go:embed` 的字节 Go 编译器不检查 | `frontend_test.go` 的语法闸门 + 冒烟 |

---

## 4. 一次改动前的自检脚本

```bash
# 这个字段/常量还有谁在用？
rg "字段名|函数名" --type go

# 有没有第二个实现？
rg "func .*jitter|Classify|VerifyBearer|UID8" --type go

# 面板是否也读这个 JSON 键？
rg "键名" internal/panel/app.js

# 测试是否已经钉住当前行为？
rg "断言里的字面量" --type go
```

改动涉及两份映射（协议 ↔ 面板、handler ↔ upstream、pool ↔ scheduler）时，**在同一批提交里一起改**，并在注释里写下"这两处必须同步"的原因。

---

## 5. 判断"这算不算跨层改动"

- 只改一个包内的私有函数：不算。
- 改了导出方法的签名或返回值语义：算（要检查所有调用方 + 测试）。
- 改了 JSON 字段名/空值语义/状态码/错误类别/冷却时长/头像或头族：算，且必须同步面板与文档。
- 改了配置字段：算（`config.go` + `config.example.json` + README + 可能的 `restartRequiredFields` + `CFG_MAP`）。
- 只改注释/格式：不算。
