---
name: backend-config-and-hot-reload
description: 配置模型、缺省值与 normalize、环境变量、原子落盘、livecfg 热快照、重启生效字段清单
paths:
  - cmd/server/**
  - internal/livecfg/**
  - internal/panel/config.go
---

# 配置与热生效

配置是网关最容易出"看起来生效其实没生效"问题的地方。本文件给出唯一正确改法。

## 配置模型（`cmd/server/config.go`）

一个 `Config` 结构体，顶层标量 + 若干匿名嵌套结构（`Panel` `Logging` `Server` `Cooldown` `Schedule` `Global` `Upstream` `Features` `Prompt` `Upstash` `Pool` `SessionSticky`），最后是 `APIKeys []apikeys.Key`。

两条硬约定：

1. **JSON 里的时长一律是字符串（`"600s"` / `"30m"`），解析结果放在 `json:"-"` 的 `...Dur` 伴随字段。** 例：`Cooldown.SoftRate` ↔ `SoftRateDur`、`SessionSticky.TTL` ↔ `SessionTTL`。新增长时字段必须照此拆成两个字段，解析统一在 `normalize()`。
2. **派生路径不加配置项。** `usage.json`、`model.json`、`output_probes.json`、`request-logs/` 都由 `state_file` 的同级目录推出（`cmd/server/main.go` 的 `stateSibling`）。改 `state_file` 时这些数据跟着走，这是有意设计。

## 缺省值 / 环境变量 / 校验三条路径

```
Default()            → 完整缺省 Config（含「缺省 true」的开关显式赋值）
  ↓
Load(path)           → Default() + 读文件 + ParseConfigInto + applyEnv + normalize()
  ↓
ParseConfigInto(raw, base) → json.Unmarshal + normalize()   // 面板保存走这条
ParseConfig(raw)     → ParseConfigInto(raw, Default())
```

- 「缺省 true」的布尔开关靠 `Default()` 里显式赋值实现：`Load` 先拿 `Default()` 再 `json.Unmarshal` 覆盖，键缺席或为 `null` 时字段保持 `true`。所以新增这类开关**必须**在 `Default()` 里写 `= true`，否则老配置行为会变。
- `applyEnv` 是逐键手写 `if v := os.Getenv("WB2A_X"); v != ""`，没有反射循环。数值用 `strconv.Atoi`、布尔用 `strconv.ParseBool`，**解析失败静默忽略**（保持缺省）。要新增环境变量就照抄一条。
- `normalize()` 是唯一的归一化点：钳制/回落、`time.ParseDuration`、`validateScheduleHours()`、`normalizePrompt()`。任何"输入合法化"都写在这里，别在消费方再判一次。
- 未知 JSON 键被自然忽略（没有 `DisallowUnknownFields`），并且有测试固定这一行为。**退役一个键 = 删字段 + 就地写一行注释说明历史**，不要改成"报错拒绝"。

## 校验错误风格

- 一律 `fmt.Errorf`，前缀是**配置里的点分路径**，底层错误用 `%w` 包装：
  `fmt.Errorf("pool.breaker_cooldown: %w", err)`、`fmt.Errorf("prompt.mode: %q 不是合法值（passthrough / custom / append）", v)`。
- 保护性数值**快速失败**，不静默钳 0：负的时长/阈值会"悄悄关掉保护"，见 `normalize()` 里的负数分支。
- 报错要给出可执行动作，例：小时越界时提示 "如要关闭该任务请设 schedule.<name>=false"。
- 「禁用」只能用显式 `*_enabled=false` 表达，**不能**用空数组 / `null` / 哨兵值；空数组的语义是"未配置 → 回落默认"。

## 首次启动自动生成

`Load` 在文件不存在时返回被 `%w` 包装的 `fs.ErrNotExist`；`main()` 用 `errors.Is` 判定（**不能用 `os.IsNotExist`**，看不穿包装），然后调 `WriteDefault(path)` 生成推荐配置（含 `crypto/rand` 随机 `api_key`）。`WriteDefault` 用 `O_EXCL` 打开，**绝不覆盖**已存在的文件；生成失败（只读目录）时降级为 `Load("")`（纯默认 + 环境变量），不致命。

## 原子落盘（面板保存路径）

`cmd/server/main.go` 的 `saveConfig` 是**唯一**的配置写入口，固定四步：

1. 读盘上原始 JSON 成 `map[string]any`，用 `mergeConfigMaps` 把表单 payload 深合并进去（嵌套对象逐键覆盖）。目的：保住面板不管理的键与用户手写的未知键。
2. `ParseConfig(mergedJSON(merged))` 走与启动完全相同的校验；校验失败直接返回 400，**一个字节都不写**。
3. 写 `path+".tmp"`（`0o600`）再 `os.Rename` 原子替换。单文件 Docker bind mount 会返回 `EBUSY`，此时回退为原地 `O_TRUNC` 覆写；回退失败时**故意保留** tmp 文件便于人工取证。
4. 热生效 + 返回重启清单（见下）。

配套规则：

- 不要为"只写密钥"另开写路径（`SaveKeys` 也复用同一条 merge+validate 链）。两条落盘路径迟早写出互相冲突的文件。
- 面板层（`internal/panel/config.go`）只做 HTTP 编排：请求体上限 1 MiB、错误映射 400、`restart_required` 为 `nil` 时归一成 `[]`。合并与校验逻辑归 `cmd/server`。

## livecfg：热生效的唯一载体

`internal/livecfg` 是**不可变快照 + `atomic.Pointer`**，无锁：

```go
type Snapshot struct { APIKey string; SoftCooldown time.Duration; SanitizeFingerprints bool; RecordClientInfo bool }
type Holder struct { p atomic.Pointer[Snapshot] }
```

- `Load()` 对 nil Holder / 从未 Store 返回零值快照，调用方无需判空。
- 快照**只收编"读路径深、热改需求强"的少数字段**。池参数、排程参数各有既有 setter（`pool.SetBreaker`、`scheduler.Reconfigure` …），不要再塞进 livecfg。
- 消费方必须在**每次请求/每次调用**时 `Load()`，绝不把值拷进长生命周期结构体字段。参考 `internal/server/handler.go` 的 `loadLive()` / `softCooldown()`、`internal/panel/panel.go` 的 `apiKey()`。
- `Live == nil` 时消费方要用静态字段兜底（`handler.loadLive()` 就是这么写的），这样裸用与测试路径拿到同一缺省值。
- 写入方只有 `saveConfig`；启动时的 `livecfg.New(...)` 字面量与 `saveConfig` 里的 `Store(...)` 字面量必须保持字段一致。

## 重启生效字段清单

`cmd/server/main.go` 的 `restartRequiredFields(c)` 返回**整类清单**而不是本次 diff，面板据此提示用户。当前包含：

```
listen, auth_dir, state_file,
upstream.timeout_seconds, upstream.header_timeout_seconds, upstream.idle_timeout_seconds,
upstream.user_agent,
cn.chat_base（非空时；装配期写进 up.ChatBaseCN）,
upstash（配置了 URL 或 Token 时）,
session_sticky.ttl, session_sticky.gc_interval,
logging.request_archive_enabled, logging.request_retention_days, logging.request_archive_max_mb,
server.read_timeout
```

这些值在装配期被监听地址 / 出站 `http.Client` / 目录句柄 / 归档 writer 捕获。**新增一个装配期字段时，必须同时把它加进这个清单**——`upstream.user_agent` 曾因漏列导致"面板显示已保存但毫无效果"（issue #102）。

### 清单目前并不完整（现状缺口，别当成已实现边界）

`restartRequiredFields` 只覆盖了**已知会被装配期捕获**的一部分字段。下列字段同样是在启动时装配、之后不再读取，但**不在**清单里，因此面板保存后会显示"已保存并立即生效"而实际不生效：

| 字段 | 装配位置 | 捕获方式 |
|------|----------|----------|
| `prompt.mode`、`prompt.file`（→ `PromptText`） | `cmd/server/main.go` → `server.Config{PromptMode, PromptText}` | 进 handler 结构体，请求期不重读 |
| `global.enabled` | `up.GlobalEnabled`、`auth.SetGlobalEnabled`、`server.Config.GlobalEnabled` | 三处一次性写入（上游闸 / auth 闸 / handler 闸） |
| `global.chat_base`、`global.billing_base` | `up.ChatBaseGlobal`、`up.BillingBaseGlobal` | 出站 base 装配后固定 |
| `upstream.client_version`、`cli_version`、`client_name` | `up.ClientVersion` / `up.CliVersion` / `up.ClientName` | 出站 UA 与归属头在装配期定型 |
| `upstream.device_token`、`device_token_file`、`passthrough_ip` | `up.DeviceToken` / `up.DeviceTokenFile` / `up.PassthroughIP` | 同上 |
| `budget.daily_limit`、`burst_limit`、`cache_low_rate` | `server.Config` → `NewHandler` 构造 `dailyBudget` | 已加进 `restartRequiredFields`（>0 时提示） |

### 2026-10 新增键的热生效状态

| 键 | 生效方式 |
|----|----------|
| `features.sanitize_brand_scope` | 热生效：`saveConfig` 写 `up.SanitizeBrandAll` |
| `models.allowed` | 热生效：进 `livecfg.Snapshot.AllowedModels` |
| `schedule.account_spread_minutes` / `account_spread_min_gap_seconds` / `night_chat_cap` | 热生效：`sch.SetAccountSpread` |
| `budget.*` | 装配期（见上表） |

写新代码时的正确规则是两条：

1. **不要**把这份清单当作"其余字段必然热生效"的证明——要判断某个字段是否真的热生效，去 `saveConfig` 里看它有没有 `live.Store(...)` 或对应 setter。
2. 新增任何"装配期捕获"的字段，**必须**同时加进 `restartRequiredFields`；顺手把上面缺口表里的项补进去更好（属于已知技术债，改它要单独一次提交并配测试）。

## 改配置字段的最小动作集

1. `cmd/server/config.go`：加字段（时长记得配 `json:"-"`）、在 `Default()` 给缺省值、需要时在 `normalize()` 校验/解析。
2. `config.example.json`：加同名键（这是最完整的字段参考）。
3. `README.md` 字段速查表：加一行（注意 README 已有 `prompt.mode` 缺省值写错的漂移，别照抄）。
4. 若为装配期字段：加进 `restartRequiredFields`。
5. 测试：在 `cmd/server/config_test.go` 补"显式值生效 + 缺省回落 + 老配置不受影响"三面断言（该项目既有习惯）。
6. 面板表单：若要暴露给面板，还需 `internal/panel/app.js` 的 `CFG_MAP` 加条目，见 [../frontend/panel-ui.md](../frontend/panel-ui.md)。

## 反模式

- 在消费方直接 `time.ParseDuration(cfg.Cooldown.SoftRate)`——必须在 `normalize()` 一次性解析。
- 把热改字段在装配时 `cfg.X` 拷进出站对象（`upstream.user_agent` 的历史事故）。
- 给 `Config` 加 `json` 标签的 `time.Duration`（序列化会变纳秒整数，破坏配置可读性）。
- 面板保存时直接 `json.Unmarshal` 到 `Config` 再整文件覆写：会丢掉未知键。
- 用 `os.IsNotExist` 判断 `Load` 的"文件不存在"。
