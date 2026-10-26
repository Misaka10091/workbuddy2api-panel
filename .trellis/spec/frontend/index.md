---
name: frontend-index
description: 内嵌面板前端规范入口：资产结构、前置清单、契约与质量门
---

# 前端规范入口（内嵌面板）

面板前端**没有构建步骤**：`internal/panel/index.html`（页面骨架 + 全部 CSS）与 `internal/panel/app.js`（全部逻辑）由 `internal/panel/index.go` 用 `go:embed` 打进二进制，浏览器按 `/panel/` 与 `/panel/app.js` 直接拿。

所以"前端规范"= 改这两个文件时该守的规矩 + 它们与 Go handler、与 Go 测试之间的三重契约。

## 文件地图

| 文件 | 管什么 | 主要管辖文件 |
|------|--------|--------------|
| [panel-assets-and-security.md](./panel-assets-and-security.md) | 内嵌与缓存、CSP 与安全头、为什么禁止内联脚本 | `internal/panel/index.go`、`index.html` |
| [panel-ui.md](./panel-ui.md) | 渲染与转义、`api()`、视图切换、配置表单 `CFG_MAP`、结构标记契约 | `internal/panel/app.js`、`index.html`、`internal/panel/*.go` |
| [testing.md](./testing.md) | 无浏览器测试策略：node 闸门、切片 + vm、冒烟测试、断言标记 | `internal/panel/*_test.go` |

后端侧（面板 Go handler、密钥、用量）见 [../backend/index.md](../backend/index.md)。

## 三重契约（改前端必看）

```
Go handler（internal/panel/*.go）
   提供 /panel/api/* 的 JSON
        ↕  字段名与空值语义（[] 不是 null、字段只增不改）
app.js（渲染 + 交互）
   通过 id / data-* 标记操作 DOM
        ↕  结构标记是测试锚点，重命名即测试失败
index.html（骨架 + CSS）
```

任何一层改动都要同步下面三处：

1. `app.js` 的渲染与事件绑定（字段名来自 Go 侧 JSON tag）。
2. `index.html` 里被 JS 依赖的 `id` / 结构（JS 只用 id 取元素，没有模板引擎）。
3. `*_test.go` 里对该标记的断言（以及 `CFG_MAP` 类的双向一致性测试）。

## 前置清单

- [ ] 确认改的是"前端资产"还是"面板 Go 接口"：只有前者落在本目录规范内。
- [ ] 新增 DOM 节点：`index.html` 给稳定 `id`，`app.js` 通过 `$('id')` 取；不要用查询后代的方式做脆弱选择。
- [ ] 新增任何插入 HTML 的地方：所有动态值必须过 `esc()`（文本与属性共用一个函数）。
- [ ] 新增面板接口字段：Go 侧空集合要返回 `[]` 而非 `null`，前端要能区分"空"与"未启用"。
- [ ] 新增配置项：`CFG_MAP` 加条目，`index.html` 的 `#cfgForm` 里加同名 `name` 控件（有双向一致性测试）。
- [ ] 新增视图：`TITLES` 加 title、`index.html` 加 `<section class="view" id="view-<name>">`、`go()` 加加载分支、测试的冒烟 hash 列表加一条。
- [ ] 改完必须让 `node --check internal/panel/app.js` 与 `go test ./internal/panel/` 都过。

## 质量门

```bash
node --check internal/panel/app.js     # 语法（Go 不检查内嵌 JS）
go test ./internal/panel/              # 结构断言 + vm 单测 + 冒烟（需 node）
go vet ./... && go build ./...
```

- 缺 node 时面板测试会整体 `t.Skip`——那时"通过"不代表 JS 被验证过。
- 面板 JS 白屏事故有过两次，`TestAppJSTopLevelSmoke` 就是为此加的：它会真实执行 `app.js` 的顶层代码。**新增视图必须进冒烟清单。**

## 本层最容易踩的三个坑

1. **内联脚本会让 CSP 直接拦掉整页**：`script-src 'self'`，没有 `unsafe-inline`。
2. **用 `div.innerHTML` 做转义**会漏引号，属性注入可以闭合 `title="…"`。
3. **顶层 `setTimeout(…, 0)` 的深链初始化不能删**：删了会让 `#usage` / `#packages` 深链踩 TDZ，表现为"该页永远读取失败"。
