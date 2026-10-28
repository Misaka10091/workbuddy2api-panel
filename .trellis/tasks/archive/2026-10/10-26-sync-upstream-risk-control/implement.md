# 实施计划与结果

## 顺序

1. P0 出站指纹（自包含，可独立回归）
   - `headers.go`：`v3ProbeUA`/`v3ProbeUALite`、`acceptLanguageFor` 恒 `zh`、`ChatHeaders`
     剥离与补齐、`injectAttribution` 的 `X-Product`、`injectGlobalChatHeaders` 去哨兵头。
   - `client.go`/`global_models.go`：探测 UA 切换、删除 `codeBuddy*UA` 常量。
   - `desktop.go`：设备指纹、`desktopHost`、`X-Request-ID`、会话 ID、`ChatHeaders` 统一。
   - `autotask.go`/`scheduler.go`/`travel.go`/`blackcat.go`/`school.go`：`wb2api-*` → `session` ID。
2. P2 状态正确性（最小、低风险）：`session.touch` CAS；`pool` 两处置脏。
3. P1 频控：
   - 新增 `scheduler/humanize.go`、`scheduler/spread.go`；
   - 五个任务入口拆 `runX(ctx, realm, spreadOn)`，排程走 `true`、手动走 `false`；
   - 余额刷新 ticker 抖动 + 账号级错峰；夜猫子 `nightCapList`；
   - 面板 `batchRunning` 闸门 + `models` 缓存（前端 `refresh=1`）；
   - 脱敏品牌词与 `sanitize_brand_scope`（payload 增加 scope 变体函数，旧函数保持 all）。
4. P3 观测与暴露面：`insecureKeyWarning`、reqlog 两字段 + 流/非流式计数、`models.allowed`、
   `budget.go` 三道闸。
5. 回归测试 + 样例配置 + 规范更新。

## 验证

每阶段跑 `go build ./...`，阶段末跑 `go test` 相关包；最终：

```
go build ./...   # 通过
go vet ./...     # 通过
go test ./...    # 16 个包全绿
```

新增测试文件：

- `internal/upstream/fingerprint_risk_test.go`：chat 头族形态、桌面指纹、上报头。
- `internal/upstream/sanitize_brand_test.go`：品牌词改写与两种作用域。
- `internal/session/touch_cas_test.go`：迟到 touch 不改绑/不复活。
- `internal/pool/sessiondead_dirty_test.go`：中间计数与清零都置脏。
- `internal/server/allowed_budget_test.go`：白名单规则、三道预算闸、CST 跨日。
- `internal/scheduler/spread_test.go`：摊开确定性、最小间隔、夜猫子上限、抖动边界。
- `internal/panel/panel_gate_cache_test.go`：闸门互斥、缓存 TTL/覆盖。
- `cmd/server/insecure_key_test.go`：弱密钥/回环监听矩阵。

## 遗留

- 上游 `X-API-Key` 双头（本地无静态 API Key 账号）与 images/search 白名单执法（本地无该路由）不适用。
- CN 对话主域改 `www.workbuddy.cn`（`741cf59`）单列待决策。
- 预算与白名单为装配期配置（`models.allowed` 经 livecfg 热改；预算需重启）。
