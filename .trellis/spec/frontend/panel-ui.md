---
name: frontend-panel-ui
description: app.js 的渲染与转义、api()、视图切换、配置表单 CFG_MAP、批量与密钥交互、结构标记契约
paths:
  - internal/panel/app.js
  - internal/panel/index.html
  - internal/panel/panel.go
  - internal/panel/apikeys.go
  - internal/panel/batch.go
  - internal/panel/config.go
---

# 面板 UI 约定

`app.js` 约 3700 行、单文件、无框架、无模块系统（`<script src="app.js">` 普通脚本，顶层 `'use strict'`）。`index.html` 是静态骨架 + 全部 CSS，JS 通过 `id` 找节点并字符串拼接生成 HTML。

## 文件内部结构（必须保持的分层）

```
第 1 行                'use strict';
文件顶部               全部模块级 let/const 状态（TDZ 纪律，见下）
── 状态 ──              LS_KEY / LS_THEME / view / 各视图筛选状态
── 主题 ──              applyTheme / effTheme
── 请求 ──              api() / toast() / esc() / ago() / copyText() / dur()
── 各视图 ──            accounts / usage / packages / taskscenter / models / apikeys / config / logs
文件末尾                setTimeout(深链初始化, 0)
```

- **模块级 `let`/`const` 必须声明在文件顶部**。原因：`go(v)` 会同步触发 `loadUsage`/`loadLogs`/`loadPackages`，这些函数读到的是文件后半段才初始化的变量——深链 `#usage`/`#packages` 打开时会踩 TDZ，表现为"该页永远读取失败，点导航进去却正常"。
- 与上一条配套：深链初始化必须包在 `setTimeout(…, 0)` 里，等整份脚本求值完再 `go()`。**这两条是一对，删掉任何一半都会复发。**

## 转义：`esc()` 是唯一入口

```js
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
```

- 文本与属性**共用**这一个函数，没有单独的属性转义版本。
- 不能用 `div.innerHTML` 代替：它转义 `<>&` 但不转义引号，拼进 `title="…"` 就能闭合属性并注入事件处理器。
- 所有来自后端的值（uid、昵称、模型名、日志行、密钥名、错误文案）进 `innerHTML` 前一律过 `esc()`；数字类先格式化再插入。
- `&` 必须最先替换，否则二次转义。

## 请求：`api(path, opts)`

```js
const r = await fetch('/panel/api/' + path, { ...opts, headers: h });
if (r.status === 401) { openKey(); throw new Error('密钥无效或未填写'); }
if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
```

- 统一前缀 `/panel/api/`；自动带 `Authorization: Bearer <localStorage['wb2api.key']>`；有 `body` 时自动 `Content-Type: application/json`。
- 401 统一弹密钥框（`openKey()`），调用方只负责 `catch` 后 `toast`。
- **不要**在业务代码里自己 `fetch`：绕开 `api()` 就绕开了 401 与错误体约定。

## 视图切换

```js
const TITLES = { accounts:'账号池', usage:'用量', packages:'积分构成', taskscenter:'任务中心',
                 models:'模型与档位', apikeys:'API 密钥', config:'配置', logs:'运行日志' };
function go(v) { … document.querySelectorAll('.view').forEach(s => s.hidden = s.id !== 'view-' + v); … }
```

新增视图的四步（缺一步就有 UI 死角）：

1. `index.html` 加 `<section class="view" id="view-<name>" hidden>`，导航加 `<a data-view="<name>">`。
2. `TITLES` 加标题。
3. `go()` 里加该视图的首次加载分支（懒加载）。
4. `frontend_test.go` 的冒烟 hash 列表加 `#<name>`（否则顶层执行错误不会被发现）。

其它视图级约定：

- 导航点击用 `history.replaceState(null, '', '#' + view)` 同步 hash，刷新后停在同一页。
- realm 筛选（`accRealm`）**只影响展示与批量按钮作用域，不改变后端选号语义**；选择持久化到 `localStorage`，读回时要就地归一（旧版脏值可能是 `"all"` / `"CN"`）。
- `realmQuery(realm)` 生成 `?realm=` 片段，所有批量触发接口都用它。

## 复制：一律走 `copyText()`

`navigator.clipboard` 只在 secure context 存在；面板典型部署是内网 `http://<IP>:<PORT>`，直接调会抛 `TypeError` 且无声失败。`copyText()` 已封装"secure 判定 → `writeText` → `execCommand` 兜底"三级降级，且降级分支保持用户手势同步（不 `await`）。**不要在新按钮里直接调 `navigator.clipboard`。**

## 展示约定

- 缺失值显示 `—`，不显示 `0` 或空串（时间用 `0001-` 前缀判定"从未发生"）。
- 时间用 `ago()`（秒/分/小时/天前），时长用 `dur()`（时分秒），体积用 KB/MB 两档。
- 主题两态翻转（浅/深），首次跟随系统偏好，选择存 `wb2api.theme`，写 `document.documentElement.dataset.theme`。
- 日志视图默认"自动滚动：开"（`logPin`）；含 `P0/P1` 之类级别词的行要能一眼区分。

## 配置表单：`CFG_MAP` 是唯一映射

```js
const CFG_MAP = { listen: ['listen'], package_detail_limit: ['panel','package_detail_limit'],
                  soft_rate: ['cooldown','soft_rate'], prompt_mode: ['prompt','mode'], … };
const CLEARABLE_CFG = new Set(['user_agent', 'prompt_file']);
```

- `CFG_MAP` 的键 = `#cfgForm` 里的 `name="…"`，值 = 配置 JSON 路径数组。`dig()` / `put()` 负责按路径读写。
- **双向一致性测试**：每个表单控件必须有映射条目，每个映射键必须有控件；缺失时保存会静默丢弃该字段。
- 空串语义分两类：普通文本"空 = 不下发"（防误清空）；`CLEARABLE_CFG` 里的"覆盖型"字段"空串照发"（清空 = 回到内置默认）。`api_key` **刻意不在**可清空集合内——清空等于关掉整个鉴权。
- 时长类字段提交前用 `DURATION_RE` 校验（Go duration 语法），非法值不提交。
- 小时数组字段（`*_hours`）按逗号/空格切分转数字数组。
- 保存成功后按 `restart_required` 长度提示："配置已保存，其中 N 项需重启进程生效" / "配置已保存并立即生效"；随后重载配置与概览，并把可能已变更的密钥写回 `localStorage`（否则下一轮轮询立刻 401）。
- 后端字段语义见 [../backend/config-and-hot-reload.md](../backend/config-and-hot-reload.md)。

## 批量与密钥交互

- 批量操作：表格必须有一列**独立的勾选列**（`<th class="pick">`），不复用 `th.mark`；工具栏 id 为 `accSelAll` / `accBatchBar` / `accBatchCount` / `btnAccClearSel`；每个动作按钮带 `data-batch="<action>"`，且 `BATCH_META` 必须覆盖后端 `batchActions` 的每个键。
- API 密钥行：**只能用服务端下发的 `id` 定位**（`data-akrow` / `dataset.akrow` / `JSON.stringify({id})`），因为列表默认返回脱敏值 `abcd******wxyz`，用它定位必然失败（"脱敏展示值当主键"是历史事故）。需要明文时走 `api_keys?reveal=1` 或创建响应里一次性返回的值。
- 每账号"一键完成"类动作在后端有 `TryLock`，重复点击返回 409。前端应把 409 文案原样 `toast`，不要重试、不要排队。
- 批量执行是后端串行、无回滚；前端只展示逐项结果，不要伪造"全部成功"。

## 结构标记契约（改名前先看测试）

这些字符串/`id` 被 `internal/panel/*_test.go` 直接断言，改名或移动位置等于测试失败：

| 标记 | 位置 | 谁在断言 |
|------|------|----------|
| `<script src="app.js"></script>`（且无内联 `<script>`） | `index.html` | `security_test.go` |
| `'use strict'` | `app.js:1` | `security_test.go` |
| `<form id="cfgForm">` 及 `name="…"` 控件 | `index.html` | `frontend_test.go` |
| `th.pick` / `accSelAll` / `accBatchBar` / `accBatchCount` / `btnAccClearSel` / `data-batch` | `index.html` + `app.js` | `batch_frontend_test.go` |
| `akVeil` / `akDoneVeil` / `btnAkNew` / `akAllow` / `akDeny` / `akPreview` / `btnAkCreate` / `data-akrow` | `index.html` + `app.js` | `apikeys_frontend_test.go` |
| `data-realm="global"` / `accRealmCnN` / `accRealmGlN` / `realmCards` | `index.html` + `app.js` | `frontend_realm_test.go` |
| 若干纯函数名与它们的起止标记 | `app.js` | 切片 + `vm` 单测（见 [testing.md](./testing.md)） |

新增/重命名这些标记时：同时改测试，并保持"恢复旧行为会让测试失败"的形状。

## 反模式

- 用 `innerHTML` 拼未转义的后端字符串（XSS 与属性闭合）。
- 在 `app.js` 里加 `import` / `export`（不是模块脚本）。
- 直接 `fetch` 或直接 `navigator.clipboard`。
- 用脱敏后的密钥展示值做行主键。
- 在视图函数里重算后端已算好的聚合（面板只展示）。
- 给 `go()` 加同步的顶层调用而不包 `setTimeout(…, 0)`。
