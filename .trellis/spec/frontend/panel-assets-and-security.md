---
name: frontend-assets-and-security
description: 内嵌资产的打包与路由、CSP 与安全响应头、缓存策略与鉴权边界
paths:
  - internal/panel/index.go
  - internal/panel/index.html
---

# 资产与安全

## 内嵌与路由

```go
// internal/panel/index.go
//go:embed index.html
var indexHTML []byte
//go:embed app.js
var appJS []byte
```

- 只有两个静态路由，且**不需要鉴权**：`GET /panel/{$}` → `index`，`GET /panel/app.js` → `appScript`。页面本身不含任何秘密，密钥只在 `app.js` 运行时从 `localStorage` 读出并发送到 `/panel/api/*`。
- `ServeHTTP` 在分发前就调 `setSecurityHeaders(w)`，所以页面、脚本、API、连 401 响应体都带同一套安全头。
- 资源是嵌进二进制的，**没有构建步骤、没有 node 依赖、没有外部 CDN**。加第三方库意味着要把它 vendored 进 `app.js`（本项目至今零第三方前端依赖，保持这样）。

## CSP 与安全头（逐条都有理由，别删）

```
default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline';
connect-src 'self'; img-src 'self' data:; form-action 'none';
frame-ancestors 'none'; base-uri 'none'

X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
```

- `script-src 'self'` 意味着**页面里不能有任何内联 `<script>` 或内联事件处理器**（`onclick="…"`）。事件绑定一律用 `$('id').onclick = …`。
- `style-src` 允许内联是**刻意的取舍**：页面有少量 `style="…"`（进度条宽度、列宽），内联样式不会导致脚本执行；但外部样式域与 `@import` 仍然禁止。
- `connect-src 'self'`：前端 `fetch` 只能打本服务，这是"面板不给外部回传数据"的硬保证。
- `form-action 'none'`：配置页是 JS 提交，页面没有真正的表单提交目标。**不要**为了"原生表单提交更简单"而放宽它。
- `frame-ancestors 'none'` + `X-Frame-Options: DENY`：防点击劫持（老浏览器走后者）。

## 缓存：两个资源都必须 `no-store`

`index.html` 与 `app.js` 都是 `Cache-Control: no-store`，理由是**没有可用的破缓存版本号**：

- `app.js` 与 `index.html` 被嵌进二进制，URL 里没有 `?v=…`。
- 升级后浏览器若继续用旧 `app.js` 配新后端，就会出现"前端死代码 + 后端契约已改"的错位（历史事故：修好了"新建的密钥删不掉"，老缓存让修复看起来没生效）。
- 面板是运维工具，一份约 190KB 脚本关缓存换"重启即生效"，这个取舍是划算的。

因此：**不要给静态资源加版本查询串**，也不要改成 `max-age`。这是有意选择，且有测试固定。

## 鉴权边界

- 面板复用网关的 `api_key` 做 Bearer 鉴权（`httpauth.VerifyBearer`，常量时间），与 `/v1/*` 完全同口径。
- 密钥从 `livecfg` 快照读（`p.apiKey()`），所以面板里改完立即生效、不用重启。
- **`api_key` 为空 = 面板无鉴权**。这是给 localhost / 内网用的便利，公网部署必须设置密钥（README 安全章节也这么写）。
- 没有密码登录、没有会话 cookie、没有 CSRF token 机制。缓解手段是：CSP + `form-action 'none'` + Bearer 放在 `localStorage` + 同源限制。**不要**引入 cookie 鉴权，那会立刻需要 CSRF 防护。
- 面板 API 的错误体固定 `{"ok":false,"error":"…"}`；成功响应一律带 `"ok": true`。

## 改动清单

| 你要做的 | 必须遵守 |
|----------|----------|
| 加内联 `<script>` / `onclick=` | 不行，CSP 会拦；用 `app.js` + `addEventListener`/`onclick` 赋值 |
| 加外部字体/CDN/图标库 | 不行（`default-src 'none'`）；图标用内联 SVG（`icoTheme` 就是范例） |
| 加新静态资源 | 需要新的 `go:embed` + 路由 + 安全头 + `no-store`，并同步 [testing.md](./testing.md) 的断言 |
| 加表单提交 | 走 `api()`（JS 提交）；不要 `action=` |
| 加版本查询串 | 不行，见上 |
