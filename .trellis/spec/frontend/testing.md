---
name: frontend-testing
description: 无浏览器测试策略：node 闸门、标记切片 + vm 单测、顶层求值冒烟、Go 侧结构断言
paths:
  - internal/panel/*_test.go
  - internal/panel/app.js
  - internal/panel/index.html
---

# 面板前端测试

没有浏览器、没有 jsdom、没有 npm 依赖、没有 Playwright。测试全部是 Go 的 `go test`，需要跑 JS 时通过 `node` 子进程。

## 三道闸门

| 闸门 | 测什么 | 需要 node？ | 位置 |
|------|--------|:---:|------|
| `node --check app.js` | 语法（对象字面量键没加引号这类会白屏的错误） | 是 | `TestAppJSSyntax` |
| 顶层求值冒烟 | 真实执行 `app.js` 顶层代码，抓 TDZ / `ReferenceError` | 是 | `TestAppJSTopLevelSmoke` |
| Go 侧结构断言 | `index.html` 结构、CSP、缓存头、`CFG_MAP` 一致性 | **否** | `security_test.go`、`TestConfigFormMatchesCFGMap` 等 |

只有**执行 JS** 的测试用 `exec.LookPath("node")` 做前置并在缺失时 `t.Skip`。**Go 侧结构断言不需要 node**：它们只是读 `index.html` / `app.js` 的字节做 `strings.Contains` 与正则匹配（`security_test.go` 全篇、`TestConfigFormMatchesCFGMap`、`TestAppJSSyntax` 以外的 index 断言都是这一类），所以在无 node 的机器上依然会真跑、真失败。

推论：无 node 时"全绿"只代表结构契约没破、**JS 一行没执行**；判断改动的 UI 行为是否可用，必须在有 node 的环境再跑一次。

## 模式一：标记切片 + `vm` 单测

`app.js` 是普通脚本，无法 `import`。测试的做法是"按函数名切片，丢进 vm 上下文跑，打印 JSON，与字面量 `want` 比对"：

```go
script := `const fs = require('fs'); const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function trimFixed');
const end   = src.indexOf('function usStat');
if (start < 0 || end < 0) throw new Error('credit helpers not found');
const ctx = { Number, String, RegExp };
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.fmtCredit=fmtCredit; /* … */', ctx);
process.stdout.write(JSON.stringify({ credit: ctx.fmtCredit(1.25), /* … */ }));`
```

- harness 写进 `t.TempDir()` 的 `*.cjs`，用 `exec.Command(node, harnessPath, "app.js")` 运行（工作目录是 `internal/panel`）。
- 期望值是一个**字面量 JSON 字符串**（含中文与 `—` 占位符），逐字节比对。
- 切片依赖**起止函数名**。所以：纯函数要保持在顶层、名字稳定、与相邻标记之间不要插入无关声明。标记找不到时 harness 主动 `throw`，测试大声失败而不是静默通过。

哪些函数值得这样测：格式化（`fmtCredit` / `fmtCreditRatio` / `fmtModelRate`）、时间解析（`parseAPITime` / 限流文案三形态）、`esc`、realm 查询串构造、配置读写辅助（`dig` / `put` / `collectConfig`）。

## 模式二：顶层求值冒烟

`TestAppJSTopLevelSmoke` 构造一个带 DOM 桩的 sandbox：

- `document` / `localStorage` / `fetch` / `matchMedia` / `navigator` 全部是**惰性 Proxy**（任何属性访问都返回自身，调用返回自身），所以不需要模拟真实接口。
- 逐个 hash 运行：当前清单是 `#taskscenter #accounts #usage #models #config #logs #packages`。
- 成功打印 `SMOKE OK` 并**显式 `process.exit(0)`**：`app.js` 顶层的 `setInterval` 会让 node 事件循环不退出。
- 失败打印 `SMOKE FAIL:` + 前 5 行堆栈，测试 `t.Fatalf` 带出原始输出。

这把闸门是为两次连续白屏加的（v1.11.3 / v1.11.4）——语法层检查看不见 TDZ，只有真执行才发现。

**现状缺口**：清单里还没有 `#apikeys`。新增视图或修深链相关问题时，顺手把它补进清单。

## 模式三：Go 侧结构断言

```go
p := newTestPanel()                                   // New(Config{Version:"test", APIKey:"test-key"})
rec := httptest.NewRecorder()
p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
if !strings.Contains(rec.Body.String(), `<script src="app.js"></script>`) { … }
```

- `newTestPanel()` 带鉴权（`APIKey: "test-key"`），这样 handler 不会因 nil 依赖 panic。
- 断言用 `strings.Contains` 找结构标记（见 [panel-ui.md](./panel-ui.md) 的标记表）。
- 要测"鉴权生效"又不想 deref 依赖时，故意请求一个不存在的路径，让鉴权层先跑。
- `security_test.go` 断言：CSP 含必需指令、**不含** `script-src 'unsafe-inline'`、`index.html` 无内联脚本、`app.js` 响应 `Content-Type` 含 `javascript`、body 含 `'use strict'`、两个资源都是 `no-store`。

## 给新 UI 行为补测试的步骤

1. 尽量把逻辑做成**顶层纯函数**（无 DOM 依赖），用模式一测。
2. 需要 DOM 结构的，加 `index.html` 的 id/结构 needle 断言（模式三）。
3. 动了配置表单 → 必须让 `CFG_MAP` ↔ `name="…"` 双向一致性测试保持绿。
4. 新增视图 → 往冒烟 hash 清单里加一条。
5. 测试要写成"**恢复旧 bug 就会失败**"的形状（仓库里对过期提示框与渲染路径都有明确注释）。

## 反模式

- 引入 jsdom / puppeteer / npm 依赖：测试必须能在只装了 Go（可选 node）的机器上跑。
- 断言"渲染后的 DOM 长什么样"：没有浏览器，只能断言源码里的标记与纯函数输出。
- 为了让测试通过而放宽 `want` 字面量（应该改代码）。
- 把纯函数挪到文件后半段或改名字——会让切片 harness 找不到标记而报错（这其实是保护，别绕过）。
- 在无 node 的环境把"跳过"当成"通过"来汇报。
