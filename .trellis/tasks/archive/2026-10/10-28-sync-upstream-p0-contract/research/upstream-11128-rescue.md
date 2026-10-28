# Research: 上游「11-128 内容审核抢救」改动技术档案（可直接照搬移植）

- Query: 提取上游 `db31fc2` / `4a577fc` / `4f79fc8` / `49f9db4`（前两项）/ `e4dd458` 的确切改动
- Scope: internal（只读 git 离线对象，未改动任何生产代码）
- Date: 2026-10-28
- 基线: 本仓上次同步的上游基线 = `5367e78`；上游远端最新 = `upstream/main`（`80e844f`）
- 结论优先级: 上游 `upstream/main` 的最终形态 > 单个 commit 的中间形态（`e4dd458` 覆盖了 `49f9db4` 的抢救顺序）

---

## 0. 十条结论（先看这里）

1. **@ 路径段**：一条**字面量预检** `"Tokens prefixed with @"`（进 `sanitizeFeatures` 表）+ 一条 `(?is)` 整段正则 + 一段同义改写替换串。正则与替换串见 §1，**逐字照贴即可**。
2. 覆盖两种线上措辞：0.3.x 当前的 `...path containing spaces.` 与更早的 `...path that contains spaces.`（正则尾部 `path (?:containing|that contains) spaces\.`）；两者开头 17 词相同，所以单条预检就够。
3. **段外 `@` 不误伤**：匹配以整句开头 `Tokens prefixed with @ are paths the user explicitly referenced.` 为锚，邮箱/`@deepseek-ai/...`/`C:\cache\@tmp` 都不含这句，且预检（`strings.Contains`）直接不命中 → 零成本返回原串。测试钉住三条（`TestDshPathsUnrelatedAtTextUntouched`）。
4. **身份句式中性化**：新增 `sanitizeIdentitySentenceRe`（`You are` + ≤3 修饰词 + 可选名词 + **七种**动词搭配 + `[^\n]*?\.(\s|$)`），替换为 `"You are a helpful AI assistant.${1}"`。`${1}` 是 RE2 没有 `(?=)` 的绕法——把句尾空白捕获后回填，否则 `assistant.` 会粘住下一句。
5. 预检 `hasIdentityFingerprint` 加了**动词字面量快路**（`strings.ToLower` + `Contains`），命中后**必须再交正则确认**，否则讲解产品的普通消息会被拖进清洗（末尾 `TrimSpace` 会裁用户空白）。
6. **enum 补 type**：塞进既有 schema 遍历（`unescapePatternLiteralEscapes` → 改名 `normalizeSchemaTree`），只在 `type` 缺失且 `enum` 非空时按 `inferEnumType` 无损推断；推不出唯一类型（混合标量 / 含 null）**保持原样**。`response_format` 天然不走（那条遍历只从 `obj["tools"]` 进）。
7. **system 三级抢救（最终顺序）**：① `SplitSystemMessages`（拆句**留在 system**）→ ② `MoveSystemToUser`（搬进首条 user）→ ③ `Rewrite(body, Degraded)`（整体替换）。`e4dd458` 把 `49f9db4` 的「搬家优先」降级为第二层，依据是 A–G 七组真机对照（§4.4）。
8. 三级共用 `splitApplied` / `movedApplied` / `degradedApplied` 三个「只试一次」标志，落点全在本仓已有的 `(*Handler).chatCompletions`；成功即 `h.degrade.Trigger()` 置降级态，后续请求直接按该形态出站。
9. **`internal/probe/probe_channel.go` 是 `//go:build ignore` 的独立 `package main`**，只 `go run` 时联网，正常 build/test/vet 完全不编译。本仓**当前没有 `internal/probe/` 目录**；建议**不移植**（见 §5.4）。
10. 本仓 `sanitize.go` 与上游当前形态**结构同构**（同名同序的表 + `sanitizeTextWith/text/brand`），@ 段与身份句式**可直接加**；`prompt.go` 需新增两个函数（`SplitSystemMessages` + `splitSentences` + `MoveSystemToUser`）；`sanitize_brand_scope` 本仓**已有**但内部表示不同（`SanitizeBrandAll bool` vs 上游 `SanitizeBrandScope string` + `brandScopeGlobal()`）。

---

## 1. DSH @ 路径段规则（`db31fc2` + `4f79fc8`）

落点文件：`internal/upstream/sanitize.go`（上游行号）。

### 1.1 三块资产（原文，逐字）

**(a) 预检表项** — `sanitizeFeatures` 第 8 项（上游 `sanitize.go:23`，本仓对应 `sanitize.go:15` 的表尾）：

```go
	"Tokens prefixed with @",                          // DSH 0.3.x 的 @ 路径说明段（唯一不含品牌词的 DSH 指纹）
```

**(b) 正则**（上游 `sanitize.go:69-70`）：

```go
var sanitizeDshPathsRe = regexp.MustCompile(
	`(?is)Tokens prefixed with @ are paths the user explicitly referenced\..*?path (?:containing|that contains) spaces\.`)
```

拼接后的单串（便于复核）：
`(?is)Tokens prefixed with @ are paths the user explicitly referenced\..*?path (?:containing|that contains) spaces\.`

**(c) 常量**（上游 `sanitize.go:72`、`:80-85`）：

```go
const sanitizeDshPathsProbe = "Tokens prefixed with @"

const sanitizeDshPathsReplacement = "Paths beginning with @ refer to files the user explicitly mentioned. " +
	"Relative paths are resolved against the project root; absolute paths point " +
	"to files or directories on the host. A trailing slash marks a directory, so " +
	"list it when its contents matter. Anything else is a file: open it with the " +
	"read tool when its contents are needed, and never claim to have inspected it " +
	"before reading. The @\"...\" form only quotes a path that contains spaces."
```

拼接后的单行替换串（**注意 `project root`，不是 `workspace root`**）：

```
Paths beginning with @ refer to files the user explicitly mentioned. Relative paths are resolved against the project root; absolute paths point to files or directories on the host. A trailing slash marks a directory, so list it when its contents matter. Anything else is a file: open it with the read tool when its contents are needed, and never claim to have inspected it before reading. The @"..." form only quotes a path that contains spaces.
```

**(d) 调用点** — `sanitizeTextWith`，位置在**品牌改写块之后、header 剥离之前**（上游 `sanitize.go:194-198`）：

```go
	// @ 路径说明段：预检已是字面量，这里再挡一次，避免无关文本白跑一遍跨行正则。
	if strings.Contains(text, sanitizeDshPathsProbe) {
		text = sanitizeDshPathsRe.ReplaceAllString(text, sanitizeDshPathsReplacement)
	}
```

→ 这一段**不在 `if brand` 里**，属全局改写层（任何角色都改）。

### 1.2 覆盖的两种线上措辞

函数注释原话（上游 `sanitize.go:58-63`）：

```
// 两种线上见过的结尾措辞都要覆盖（(?s) 让 .*? 跨行，段内本来就有换行）：
//
//	... quotes a path containing spaces.            （0.3.x 当前写法）
//	... form only quotes a path that contains spaces. （更早的写法）
```

两种措辞的**测试夹具原文**（上游 `sanitize_test.go` 的 `dshPathsNow` / `dshPathsOld`，移植时直接复用）：

```go
	dshPathsNow = "Tokens prefixed with @ are paths the user explicitly referenced. " +
		"Relative paths resolve from the workspace root; absolute paths point to " +
		"files or directories on the host. A trailing slash marks a directory, so " +
		"list it when its contents matter. Anything else is a file: open it with " +
		"the read tool when its contents are needed, and never claim to have " +
		"inspected it before reading. The @\"...\" form only quotes a path that " +
		"contains spaces."
	dshPathsOld = "Tokens prefixed with @ are paths the user explicitly referenced. " +
		"Relative paths resolve from the workspace root; absolute paths identify " +
		"files or directories on the host. A trailing slash marks a directory: " +
		"list it when its contents matter. Anything else is a file: use the read " +
		"tool when its contents are needed, and do not claim to have inspected it " +
		"before reading. @\"...\" quotes a path containing spaces."
```

差异只在中间措辞与结尾：`absolute paths point to` / `absolute paths identify`；`The @"..." form only quotes a path that contains spaces.` / `@"..." quotes a path containing spaces.`。正则靠 `.*?` 吞掉中间、靠两个分支吃下结尾，因此**一条正则覆盖两种**。

### 1.3 段外 `@` 如何避免误伤

语义上：正则锚定在整句 `Tokens prefixed with @ are paths the user explicitly referenced.`，任何不含这句的文本都匹配不上；且外层还有**字面量预检**，不命中就连正则都不跑（`hasFingerprint` → 直接 `return text`，零分配）。测试钉住三类段外文本（`TestDshPathsUnrelatedAtTextUntouched`）：

```go
		"Mail me at someone@example.com and open C:\\cache\\@tmp.",
		"npm i @deepseek-ai/dsh-llm",
		"Relative paths resolve from the workspace root, but this is not the paragraph.",
```

第三条是关键时刻的守卫：**光有「Relative paths resolve from the workspace root」不算指纹**，必须整段齐（这条断言同时钉住「只发首句 200 / 前两句连排 400」的实测结论）。

### 1.4 大小写敏感性 & 作用角色范围（注释原话）

上游 `sanitize.go:65-67`：

```
// 两条已知边界：预检是大小写敏感的（与 sanitizeFeatures 其它项一致；DSH 原文大小写固定）；
// 规则属于全局改写层，任何角色里出现这一段都会被改——上游按连续原文匹配、不分角色，
// 保留原样必然 400。
```

也就是：
- **预检 `strings.Contains` 大小写敏感**（`sanitizeFeatures` 全线如此）；正则本身带 `(?i)`，但预检不过就永远跑不到正则 → 实际等价大小写敏感。
- **角色范围 = 全部**（system/developer/user/assistant/tool_calls/reasoning 都走 `sanitizeTextWith(..., brand)`，@ 段在 `if brand` 之外）。

为什么必须整段一起改（`sanitize.go:53-57`）：

```
// 上游按「连续原文」匹配（逐段二分实测：只发首句放行，前两句连排即拦），所以必须整段
// 一起改。这里做同义改写而不是删除：@ 路径、相对路径基准、结尾斜杠表示目录、其余按文件读、
// @"..." 用于含空格的路径——语义全部保留，只换措辞让连续特征失配。
```

### 1.5 `4f79fc8` 把 `workspace root` 改成 `project root` 的理由

`4f79fc8` 的 diff 注释（上游 `sanitize.go:76-79`）与 commit message 第 3 点：

```
// 一处偏离：原文用 "workspace root"，这里写 "project root"。ithtelab/workbuddy-manager#312
// 的 0032 声称 2026-10-27 上游又单独加了 "workspace root" 这个词（root→base 即放行），
// 与上面的整段回放结论冲突——两种说法下 project root 都安全，且与 DSH 语义等价，故取它。
```

来源两说冲突、取交集：
- `workbuddy2api-hub #677/#678` 的**整段回放**结论：改整段即 200，`workspace root` 保留没问题；
- `ithtelab/workbuddy-manager#312 的 0032`：称上游又单独拉黑了 `workspace root`（`root → base` 即放行）；
- 两条说法下 `project root` **都安全**，且与 DSH 语义等价（相对路径基准）→ 用 `project root` 消掉不确定性。**移植时按 `project root` 走**，别回退成 `workspace root`。

### 1.6 commit 里的实测/边界声明（`db31fc2` message）

- 现场：DSH 0.3.x system prompt 的这整段被上游按**连续原文**拦；只发首句 200，前两句连排即 `400 11-128 Illegal API invocation from an unapproved channel`。
- 这一段是**唯一不含品牌词**的 DSH 指纹 → 身份类规则（`DeepSeekRuntime`）的预检**根本不会命中**，原有 8 条字面量特征也一条都对不上，原文原样出站。
- 替换串与 `workbuddy2api-hub #677/#678` **逐字一致**，那段已在真实 CN 账号回放过（修复前 400 / 修复后 200）。
- 验证：`go build` / `go vet` / `go test ./...` 全绿；新增 7 个用例：两种措辞、跨行、嵌段保留前后文、幂等、段外 `@` 不动、`prepareBody` 全链路。

---

## 2. 身份句式中性化（`4f79fc8`）

落点：`internal/upstream/sanitize.go`（上游 `:87-115` 定义，`:186-192` 调用，`:226-244` 预检）。

### 2.1 正则原文（上游 `sanitize.go:103-108`）

```go
var sanitizeIdentitySentenceRe = regexp.MustCompile(
	`(?i)You are\s+` +
		`(?:an?\s+|the\s+)?(?:[\w.-]+\s+){0,3}?` +
		`(?:agent|assistant|model|AI|LLM)?[\s,]*` +
		`(?:powered by|built on|built by|created by|developed by|made by|trained by)` +
		`[^\n]*?\.(\s|$)`)
```

拼接单串（**移植时按这一行核对**）：

```
(?i)You are\s+(?:an?\s+|the\s+)?(?:[\w.-]+\s+){0,3}?(?:agent|assistant|model|AI|LLM)?[\s,]*(?:powered by|built on|built by|created by|developed by|made by|trained by)[^\n]*?\.(\s|$)
```

### 2.2 替换串与捕获组写法（上游 `sanitize.go:110`）

```go
const sanitizeIdentitySentenceReplacement = "You are a helpful AI assistant.${1}"
```

注释原话（`sanitize.go:99-102`）：

```
// 结尾用捕获组 (\s|$) 回填，而不是零宽断言：Go 的 RE2 不支持 (?=)，句尾那个空白必须
// 原样接回去——否则 "…assistant. Use the read tool" 会被粘成 "assistant.Use the read tool"。
// shortcut: 句子必须以句号收尾才匹配（观测到的指纹都带句号），无句号的变体放行。
```

### 2.3 七种动词搭配（同一张表两处维护）

正则内：`powered by` / `built on` / `built by` / `created by` / `developed by` / `made by` / `trained by`
字面量快路（上游 `sanitize.go:113-115`）：

```go
var sanitizeIdentityVerbs = []string{
	"powered by", "built on", "built by", "created by", "developed by", "made by", "trained by",
}
```

为什么一次列七种（`sanitize.go:105-106`）：`上游补一条新搭配就能让只认单个动词的实现重新 400，故一次列七种。`

### 2.4 修饰词窗口 ≤3（`(?:[\w.-]+\s+){0,3}?`）+ 上界的作用

`sanitize.go:107-108`：

```
// 窗口有界（形容词 0~3 个、名词可省）——`You are free to use tools built by …` 这类
// 分词修饰宾语的句子不该被误伤，靠的就是这个上界。
```

`{0,3}?` 是**懒量词**：先试 0 个修饰词，不匹配再逐步放宽（配合后面的可选名词与动词表）。`[\w.-]+` 允许带点/连字符的专名（`deepseek-v4.1-flash`、`AcmeRuntime`）。

### 2.5 `hasIdentityFingerprint` 新增的动词字面量快路（上游 `:226-244`）

```go
// hasIdentityFingerprint 身份类特征（品牌词 / 身份句式）预检；只在需要应用身份类规则时调用，
// 保证 brand=false 的路径不会因为只含品牌词而进入清洗（否则末尾 TrimSpace 会裁掉用户空白）。
//
// 身份句式先走动词字面量探测（纯 C 单遍扫描），命中才交给正则确认——正则确认是必需的：
// 只按 "powered by" 这类词判真会把「讲解某产品」的普通用户消息也拖进清洗。
func hasIdentityFingerprint(text string) bool {
	for _, f := range sanitizeIdentityFeatures {
		if strings.Contains(text, f) {
			return true
		}
	}
	low := strings.ToLower(text)
	for _, v := range sanitizeIdentityVerbs {
		if strings.Contains(low, v) {
			return sanitizeIdentitySentenceRe.MatchString(text)
		}
	}
	return false
}
```

配合调用点在 `sanitizeTextWith` 的 `if brand` 块**首行**（上游 `:188-192`）：

```go
	if brand {
		// 身份句式先于品牌词：正则要匹配原文里的品牌全名，先换品牌会让规则失配。
		text = sanitizeIdentitySentenceRe.ReplaceAllString(text, sanitizeIdentitySentenceReplacement)
		for _, rw := range sanitizeIdentityRewrites {
			text = strings.ReplaceAll(text, rw[0], rw[1])
		}
	}
```

**顺序不可调**：先中性化整句（正则要吃原文里的品牌全名），再 `DeepSeek Harness → DeepSeekRuntime`。

### 2.6 误伤守卫覆盖的四类句子（`TestIdentitySentenceFalsePositives`）

```go
		"You are free to use tools built by the vendor.",
		"You are a helpful assistant that uses services developed by others. Ok.",
		"You are welcome to deploy packages made by third parties.",
		"You are right, that module was built by the other team. Fine.",
```

断言两条：`hasIdentityFingerprint(in)` 必须 **false**（不白跑 TrimSpace）、`sanitizeTextIdentity(in) == in`（不改写）。
机制：`You are` 后必须紧跟（冠词 + ≤3 修饰词 + 可选名词 + 动词搭配）；`free to use tools built by` 修饰词超过 3 个 / `that uses services developed by`、`welcome to deploy packages made by` 同理 → 窗口上界挡掉。

### 2.7 变体覆盖（8 条，`TestIdentitySentenceVariants`）

```go
		"You are an AI agent powered by AcmeRuntime. Next.",
		"You are a coding agent powered by the deepseek-v4.1-flash model. Next.",
		"You are an AI assistant built on DeepSeek. Next.",
		"You are Acme Agent, built by Acme Corp. Next.",
		"You are the FooBot assistant, created by someone. Next.",
		"You are a bot developed by someone. Next.",
		"You are a tool made by someone. Next.",
		"You are a model trained by someone. Next.",
```

每条断言：输出以 `"You are a helpful AI assistant."` 开头，且 `Next.` 仍在（句后文本不丢）。

### 2.8 作用域（`TestIdentitySentenceScope`）

```go
	in := "You are an AI agent powered by AcmeRuntime. Go."
	sanitizeText(in)         == in   // brand=false 不改（全局层不碰身份句）
	sanitizeTextIdentity(in) != in   // 身份路径要改
```

即：身份句式**只受 `sanitize_brand_scope` 控制**；`all`（默认）时连 user 消息里的身份句也会被中性化（`TestBrandScopeAllRewritesUserMessages` 的口径），`identity` 时只动 system/developer。

---

## 3. enum 补 type 推断（`49f9db4` 第 1 项）

落点：`internal/upstream/payload.go`。

### 3.1 推断规则（`inferEnumType`，上游 `payload.go:677-705`）

```go
// inferEnumType 从 enum 取值推断 JSON Schema 类型；推不出唯一类型时返回空（不改写）。
// 数字经 encoding/json 解出来一律是 float64，故按「全为整数值」区分 integer / number。
func inferEnumType(values []any) string {
	var strs, nums, ints, bools int
	for _, v := range values {
		switch x := v.(type) {
		case string:
			strs++
		case bool:
			bools++
		case float64:
			nums++
			if x == math.Trunc(x) {
				ints++
			}
		default:
			return "" // null / 对象 / 数组：类型不唯一，不猜
		}
	}
	n := len(values)
	switch {
	case strs == n:
		return "string"
	case bools == n:
		return "boolean"
	case nums == n && ints == n:
		return "integer"
	case nums == n:
		return "number"
	}
	return ""
}
```

| enum 取值集合 | 补齐结果 |
|---|---|
| 全 `string` | `string` |
| 全整数（`1,2`） | `integer` |
| 含小数（`1, 2.5`） | `number` |
| 全 `bool` | `boolean` |
| 混合标量（`"a", 1`）或含 `null`/对象/数组 | **原样，不补** |

注意 `case nums == n && ints == n` 在 `nums == n` **之前**（Go 的 `switch` 自上而下）。`bool` 分支放在 `integer` 之前也无影响（互斥计数）。

### 3.2 插入位置（`normalizeSchemaTree`，上游 `payload.go:637-673`）

改名 + 插入都在这里：`unescapePatternLiteralEscapes` → **`normalizeSchemaTree`**（两处调用点同步改：`payload.go:627` `fn["parameters"]`、`:629` `tool["parameters"]`）。

```go
func normalizeSchemaTree(node any) {
	switch n := node.(type) {
	case map[string]any:
		if p, ok := n["pattern"].(string); ok && strings.Contains(p, `\_`) {
			n["pattern"] = strings.ReplaceAll(p, `\_`, `_`)
		}
		if props, ok := n["patternProperties"].(map[string]any); ok {
			/* …原逻辑不变… */
		}
		// enum 缺 type：Kimi 系对参数 schema 严格校验，直接 400 11133「Invalid
		// request parameters」——同一个模型对带这类 schema 的客户端等于全废。类型能无损推断就补上，
		// 推断不出来（混合标量/含 null）保持原样，让上游按原语义报错，不猜。
		if _, hasType := n["type"]; !hasType {
			if e, ok := n["enum"].([]any); ok && len(e) > 0 {
				if t := inferEnumType(e); t != "" {
					n["type"] = t
				}
			}
		}
		for _, v := range n {
			normalizeSchemaTree(v)
		}
	case []any:
		for _, v := range n {
			normalizeSchemaTree(v)
		}
	}
}
```

**插入位置的关键点**：在 `patternProperties` 处理之后、递归 `for _, v := range n` **之前**（递归自然覆盖 `items` / `anyOf` / `$defs` / 数组形态，见 `TestToolSchemaEnumTypeNested`）。只在 `type` 缺失时补，故显式写了的（如 `{"type":"string","enum":["p"]}`）不动；`enum` 值一字不改。

### 3.3 为什么 `response_format` 不走这条

两重原因：
1. **入口不同**：这条遍历只从 `normalizeToolPatterns`（`obj["tools"]` → `tools[].function.parameters` / 裸 `tools[].parameters`）进入，`response_format` 不在遍历范围（`TestToolSchemaEnumTypeLeavesResponseFormat` 钉住 `"enum":["a"]` 原样、且不得出现 `"k":{"type":"string",...}`）。
2. **语义不同**（commit message 原话）：`response_format 不走这条（那是输出约束，补错类型会改语义）`——tools 的 enum 是**入参契约**（补类型只是让上游校验通过），response_format 的 enum 是**输出约束**（补错类型会改变模型被约束的方式）。

顺带：`normalizeToolPatterns` 与 `sanitize` 解耦（即使 `sanitize=false` 也照常归一一贯如此），本仓注释已写（`payload.go:417`）。

---

## 4. system 三级抢救（`49f9db4` 第 2 项 + `e4dd458` 重排）

### 4.1 `SplitSystemMessages`（上游 `prompt.go:96-158`，首选层）

判据与失败条件（函数注释原文，`prompt.go:96-107`）：

```
// SplitSystemMessages 把最长的那条 system/developer 消息按句子边界拆成多条 system 消息，
// 用于 11-128 的**首选**抢救：上游按「连续原文」匹配指纹，句界一断就失配，而文本一个字都
// 不改、承载通道仍是 system（模型的指令优先级不变）。
// …
// 返回 false 的两种情况：没有任何一条 system 能切出 ≥2 段（单句 system 没法拆，交给
// 调用方走后面的补救层）。
```

核心代码（上游 `prompt.go:108-158`）：

```go
func SplitSystemMessages(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, false
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, false
	}
	// 只拆最长的一条：短 system 拆开反而是把一句指令切成两半（实测人格句拆两条就失效）。
	best, bestLen := -1, 0
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		txt, ok := mm["content"].(string)
		if !ok || len(txt) <= bestLen {
			continue
		}
		best, bestLen = i, len(txt)
	}
	if best < 0 {
		return body, false
	}
	parts := splitSentences(msgs[best].(map[string]any)["content"].(string))
	if len(parts) < 2 {
		return body, false
	}
	rebuilt := make([]any, 0, len(msgs)+len(parts)-1)
	rebuilt = append(rebuilt, msgs[:best]...)
	for _, p := range parts {
		rebuilt = append(rebuilt, map[string]any{"role": "system", "content": p})
	}
	rebuilt = append(rebuilt, msgs[best+1:]...)
	obj["messages"] = rebuilt
	out, err := json.Marshal(obj)
	if err != nil {
		return body, false
	}
	return out, true
}
```

**只拆最长一条的判据**：遍历第一条到末条，`role ∈ {system, developer}` 且 `content` 是 `string`、`len(txt) > bestLen`（严格大于 → **先出现者胜**，等长不换人）才更新 `best`。找不到 → `best < 0` → false。

**为什么短 system 拆开会废掉指令**（注释 `:126` + G 组实测）：`短 system 拆开反而是把一句指令切成两半（实测人格句拆两条就失效）`。`TestSplitSystemMessagesOnlyLongest` 钉住：`["You are a pirate.", long3句, user]` → `msgs[0]` 原样，总数 5。

**句界切分规则**（`splitSentences`，上游 `prompt.go:161-190`）：

```go
// splitSentences 按句末标点切分，保留标点；句界后面的空白丢掉（消息边界本身就是分隔）。
// shortcut: 只认 . ! ? 后的空白与换行，不处理列表/代码块里的「句号」——多切一刀无害，
// 少切一刀才致命，所以宁可切碎。
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '.', '!', '?':
			// 后面紧跟空白才算句界（"3.5" / "v1.2.3" / "example.com" 不切）。
			if i+1 < len(text) && (text[i+1] == ' ' || text[i+1] == '\n' || text[i+1] == '\t') {
				if seg := strings.TrimSpace(text[start : i+1]); seg != "" {
					out = append(out, seg)
				}
				start = i + 1
			}
		}
	}
	if seg := strings.TrimSpace(text[start:]); seg != "" {
		out = append(out, seg)
	}
	return out
}
```

规则：`bytes.ToLower` 无关；`[.!?]` **后必须紧跟** `' '` / `'\n'` / `'\t'`；标点留在段尾；段两端 `TrimSpace`；空白丢弃。`TestSplitSentencesIgnoresNonSentenceDots`：`"Use version 1.2.3 from example.com then stop. Next one."` → 2 段（`1.2.3` / `example.com` 不切）。

### 4.2 `MoveSystemToUser`（上游 `prompt.go:191-258`，第二层）

判据与失败条件（注释 `:191-202`）：

```
// MoveSystemToUser 把 system/developer 的**原文**搬进首条 user 消息前部，system 换成中性
// 提示词，作为 11-128 的**后一层**抢救（前一层是 SplitSystemMessages：留在 system 更好，
// 拆不动才搬家）。返回 false = 这条请求结构不安全，别动。
// …
// 保守放弃的四种情况：没有 system/developer 文本、system 内容不是纯文本、找不到 user
// 消息、首条 user 不是纯文本（多模态 parts 里插文本会打乱顺序）。
```

实现要点（`prompt.go:203-258`）：

```go
func MoveSystemToUser(body []byte, neutral string) ([]byte, bool) {
	if len(body) == 0 || neutral == "" {
		return body, false
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, false
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, false
	}
	first, ok := msgs[0].(map[string]any)
	if !ok {
		return body, false
	}
	switch role, _ := first["role"].(string); role {
	case "system", "developer":
	default:
		return body, false
	}
	sysText, ok := first["content"].(string)
	if !ok || strings.TrimSpace(sysText) == "" {
		return body, false
	}
	for _, m := range msgs[1:] {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role != "user" {
			continue
		}
		txt, ok := mm["content"].(string)
		if !ok {
			return body, false // 多模态 parts：插文本会打乱 part 顺序
		}
		mm["content"] = sysText + "\n\n" + txt
		first["role"] = "system"
		first["content"] = neutral
		out, err := json.Marshal(obj)
		if err != nil {
			return body, false
		}
		return out, true
	}
	return body, false
}
```

失败清单（`TestMoveSystemToUserBails` 8 条 + `NeedsNeutral` 1 条）：空 body、无 messages、首条不是 system/developer、system content 非 string、system content 全空白、找不到 user、首条 user 是多模态 parts、`neutral == ""`、坏 JSON。所有失败路径都**原样返回 body**（`string(out) == in`）。
拼接形态固定为 `sysText + "\n\n" + txt`；首条 role 强制写成 `"system"`（`developer` 也被写成 `system`）。

### 4.3 三层调用顺序（最终态，`e4dd458` 定的）

`prompt.Rewrite(body, prompt.Degraded)` 仍是第三层（本仓已有），**调用顺序**：

| 序 | 层 | 函数 | 文本是否改动 | 承载通道 |
|---|---|---|---|---|
| ① | 拆句 | `prompt.SplitSystemMessages(body)` | **一字不改**（只按句界切） | 仍是 system |
| ② | 搬家 | `prompt.MoveSystemToUser(body, prompt.Degraded)` | 一字不改（只换通道） | system → 首条 user |
| ③ | 替换 | `prompt.Rewrite(body, prompt.Degraded)` | **丢掉**客户端 system 原文 | system 换成 `Degraded` |

`49f9db4` 原本是「搬家优先」，`e4dd458` 改为「拆句优先」，理由见 §4.4 的 D/E/F/G 与 commit message：

```
// 所以：① 「搬家」并不是保住人格的必要手段（D/E 持平，之前那版注释把它说过了头）；
// ② 拆句能留在 system，省掉通道降级，改动也更小。
```

### 4.4 真机实测 A–G 七组对照表（`e4dd458` message；探针 `internal/probe/probe_channel.go`，CN 账号，`deepseek-v4.1-flash`）

| 组 | 变体 | 结果 | 解读 |
|---|---|---|---|
| A | 原始 @ 路径段整段在 system | **400 11-128** | 证明拦的就是这段连续原文 |
| B | 同一段同义改写后仍在 system | **200** | sanitize 层就够（§1） |
| C | 同一段搬到首条 user + 中性 system | **200** | 搬家方案效果（无需） |
| D | 人格句在 system | **ARRR 2/3** | 人格保留（采样 3 次，2 次带 ARRR） |
| E | 同一人格句搬到 user | **ARRR 2/3** | 与 D **持平** → 搬家并不更能保人格 |
| F | 同一段按句界拆成两条 system | **200** | 拆句能过，且仍在 system |
| F2 | 一句一条 system | **200** | 逐句拆也行 |
| G | 人格句拆成两条 system | **ARRR 0/3** | **短句拆开会废掉指令**（人格丢失） |

结论：拆句「能不能留在 system」= **能，只要把连续原文断开**；而**只拆最长那条**正好避开 G 组（人格句通常短、不拆）。

端到端复核（关掉净化逼上游真回 400，走本地二进制打真上游，`e4dd458` message）：

```
日志 `content-blocked (system fingerprint) -> split system into sentences, retry`
→ 客户端拿到 200 与正文 "OK"。
```

### 4.5 「置降级态」状态机在 `handler.go` 的落点

函数：`(*Handler).chatCompletions`（本仓 `internal/server/handler.go:839`；上游 `handler.go:774`）。

**(a) 降级期内的起手形态**（上游 `handler.go:1077-1099`；本仓 `handler.go:1089-1097`）：

```go
	degradedApplied := false
	// splitApplied / movedApplied 11-128 的两层「不改文本」抢救各用一次（拆句留在 system、
	// 搬家进 user），与 degradedApplied（中性提示词替换）按顺序各用一次，避免同请求反复重试。
	splitApplied := false
	movedApplied := false
	if h.cfg.PromptMode == "custom" && h.cfg.PromptText != "" {
		body = prompt.Rewrite(body, h.cfg.PromptText)
	} else if h.cfg.PromptMode == "append" && h.cfg.PromptText != "" && !h.degrade.Active() {
		body = prompt.Append(body, h.cfg.PromptText)
	} else if (h.cfg.PromptMode == "passthrough" || h.cfg.PromptMode == "append") && h.degrade.Active() {
		// 降级期：与下面重试阶梯同一优先级——拆句留在 system 最好，其次搬家，都不行才整体替换。
		if split, ok := prompt.SplitSystemMessages(body); ok {
			body = split
			splitApplied = true
		} else if moved, ok := prompt.MoveSystemToUser(body, prompt.Degraded); ok {
			body = moved
			movedApplied = true
		} else {
			body = prompt.Rewrite(body, prompt.Degraded)
			degradedApplied = true
		}
	}
```

**(b) 撞 400 后的重试阶梯**（上游 `handler.go:1330-1371`；本仓对应位置 `handler.go:1356-1369`）：

```go
			// 内容拦截误报（passthrough/append 模式）：三层抢救，从「改动最小」到「改动最大」。
			//   ① SplitSystemMessages：按句界把 system 拆成多条，**仍在 system 里**，
			//      文本一字不改（实测原段 400 / 拆开后 200，见 prompt 包注释）；
			//   ② MoveSystemToUser：拆不动（单句 system）时把原文搬进首条 user；
			//   ③ Rewrite(Degraded)：中性提示词整体替换，保证不再命中。
			// 都被拦（用户内容本身触发审核）→ 回内容防火墙错误（见下分支）。
			// 内容问题非账号问题：applyErrorPolicy 不罚账号（见 ErrContentBlocked 分支）。
			if kind == upstream.ErrContentBlocked && (h.cfg.PromptMode == "passthrough" || h.cfg.PromptMode == "append") && !splitApplied {
				if split, ok := prompt.SplitSystemMessages(body); ok {
					body = split
					splitApplied = true
					// 置降级态：这条客户端的 system 每次都会被拦，逐请求重试等于双倍上游调用。
					// 之后的新请求直接按拆分形态出站，文本不丢也不再撞墙。
					h.degrade.Trigger()
					delete(tried, acct.UID)
					releaseHeld()
					log.Printf("content-blocked (system fingerprint) -> split system into sentences, retry")
					continue
				}
			}
			if kind == upstream.ErrContentBlocked && (…) && !movedApplied { /* 第二层，日志见下 */ }
			if kind == upstream.ErrContentBlocked && (…) && !degradedApplied { /* 第三层，原有 */ }
```

- **触发条件（共同前置）**：`kind == upstream.ErrContentBlocked` **且** `h.cfg.PromptMode` ∈ {`passthrough`, `append`} **且** 该层标志未置位。
- **三条日志串**（可直接 grep 定位现网行为）：
  - ① `content-blocked (system fingerprint) -> split system into sentences, retry`
  - ② `content-blocked (system fingerprint) -> moved system text into first user message, retry`（`49f9db4` 加的；`e4dd458` 未改）
  - ③ 原有：`content-blocked (likely fingerprint false positive) -> degraded prompt retry`（本仓 `handler.go:1367`）
- **状态机本体**：`internal/server/degrade.go` 的 `degradeGate`（`Active()` `:22`、`Trigger()` `:30`、`nextMidnightCST` `:46`）。语义：进程内存、`Trigger` 置到**次日 00:00 CST**，降级期内 `Active()==true`；不续期（保持最早触发点的重置语义）。**注意**：`Trigger()` 只有「窗口」语义，「记得上次用了哪层」完全靠每次请求重新跑 §4.5(a) 那段 if/else 链（拆不动自然掉到搬家/替换）——**没有持久化的层级状态**。
- 每次抢救成功后都会 `delete(tried, acct.UID)` + `releaseHeld()`，让同一账号拿到重试名额（单账号池也能重试，重试占一次名额）。

---

## 5. `internal/probe/probe_channel.go` 真机探针

### 5.1 形态与隔离

| 项 | 值 |
|---|---|
| 文件 | `internal/probe/probe_channel.go`（上游新增，228 行） |
| 首行 | `//go:build ignore` ← **永不参与普通构建/测试/vet** |
| 包 | `package main`（目录 `internal/probe/`，与 `probe169.go` 并列，同为 `//go:build ignore`） |
| 联网 | 只有手动 `go run internal/probe/probe_channel.go -auths … -realm cn` 时才会 |
| 本仓现状 | **`internal/probe/` 目录在本仓不存在** |

`//go:build ignore` + 目录内独立 `package main` 双重隔离：`go build ./...` / `go test ./...` 都不编译它（`ignore` 标签让它永远被排除），也不会有「两个 main 包」冲突（同一目录只有一个文件可见）。

### 5.2 依赖的配置/凭证

```
go run internal/probe/probe_channel.go -auths /path/to/auths -realm cn [-model deepseek-v4.1-flash]
```

- `-auths`：**auth 文件目录**，读 `*.json` → `auth.Parse(raw)`；跳过目录/非 json/解析失败/无 `AccessToken`/realm 不匹配/已过期（`ExpiresAt`）。
- `-realm`：`cn` / `global`（`a.IsGlobal() != (realm == "global")` 过滤）。
- `-model`：裸模型名，空 = `deepseek-v4.1-flash`。
- 运行期硬编码：`c := upstream.New(); c.SanitizeBrandScope.Store("all")`；每个变体再 `c.SanitizeFingerprints.Store(v.sanitze)`。
- 出站调用：`c.ChatStream(a, body, "127.0.0.1", upstream.ChatMeta{})`（**走网关真实出站管线**），`max_tokens:48`、`stream:true`，200 时从 SSE 里捞 `delta.content`。
- 只打印状态码与上游错误原文（截断 150 字符）+ uid 前 8 位，**不打印 token**。

### 5.3 它到底测什么

8 个变体（A/B/C/D/E/F/F2/G，见 §4.4），同一账号、同一模型、同一句话，只改**承载位置**：

```go
	cases := []variant{
		{"A 原始@段在system", dshPaths, "Say OK.", nil, false, "400/11-128", 1},
		{"B 改写@段在system", dshPathsRewritten, "Say OK.", nil, true, "200", 1},
		{"C 原始@段搬到user", neutral, dshPaths + "\n\nSay OK.", nil, false, "200", 1},
		{"D 人格在system", persona, "Say OK.", nil, true, "含 ARRR", 3},
		{"E 人格搬到user", neutral, persona + "\n\nSay OK.", nil, true, "含 ARRR", 3},
		{"F 首句/其余拆两条system", sent1, "Say OK.", []string{sent2 + " " + sent3 + " " + sent4 + " " + sent5}, false, "200", 1},
		{"F2 每句一条system", sent1, "Say OK.", []string{sent2, sent3, sent4, sent5}, false, "200", 1},
		{"G 人格拆两条system", "You are a pirate.", "Say OK.", []string{"You must begin every reply with the word ARRR."}, true, "含 ARRR", 3},
	}
```

其中 `persona = "You are a pirate. You must begin every reply with the word ARRR."`（看回答里有没有 `ARRR` 判人格在不在），`repeat:3` 只给 D/E/G 这类看比例的。
文件内还各存了一份 `dshPaths`（DSH 原文）与 `dshPathsRewritten`（网关替换串）常量——**注意 `dshPathsRewritten` 已是 `project root` 版**，可作为 §1.1(c) 的独立核对副本。

### 5.4 本仓要不要移植：建议 **不移植**

理由（按重要性）：
1. **本仓 `internal/probe/` 不存在**，上游的 `probe169.go` 也没有；引入等于新增一个维护面。
2. 它只提供一次性结论（A–G 已在 §4.4 定档），结论已经进了代码注释与用例；探针本身对回归无价值（回归由 `split_test.go` / `handler_test.go` 承担）。
3. 需要真实 CN 账号与 auth 目录 → 无法在 CI 跑，长期是死代码。
4. 若将来确实要复跑：`ChatStream(a, body, clientIP, meta)` 签名、`auth.Parse/Realm/IsGlobal/ExpiresAt`、`upstream.New()`、`ChatMeta` 本仓**全部存在且同签名**，唯一要改的是 `c.SanitizeBrandScope.Store("all")` → 本仓是 `c.SanitizeBrandAll.Store(true)`（见 §6.3）。所以「想跑时再从上游拷一次」成本极低。

---

## 6. 本仓落地差异（`internal/upstream/sanitize.go` / `internal/prompt/prompt.go`）

### 6.1 本仓 sanitize.go 现状（271 行，结构 = 上游 `5367e78` 时代）

| 本仓符号 | 行 | 说明 |
|---|---|---|
| `sanitizeFeatures`（8 条） | 15 | 字面量预检表 |
| `sanitizeIdentityFeatures`（`"DeepSeek Harness"`） | 28 | 身份类预检表 |
| `sanitizeHdrRe` / `sanitizeBareHdrRe` / `sanitizeKvRe` | 33 / 43 / 46 | 剥离层 |
| `sanitizeRewrites`（5 组） | 57 | 全局逐字替换（含 `11128 → 11-128`） |
| `sanitizeIdentityRewrites`（1 组） | 91 | `DeepSeek Harness → DeepSeekRuntime` |
| `sanitizeText` / `sanitizeTextIdentity` | 102 / 106 | brand=false / true 入口 |
| `sanitizeTextWith(text, brand)` | 112 | 管线主体：rewrites → `if brand`{identityRewrites} → hdr → kv → bareHdr → TrimSpace |
| `hasFingerprint` | 144 | 预检（表 + bareHdrRe） |
| `hasIdentityFingerprint` | 155 | **只有品牌词循环**，无句式快路 |
| `sanitizeContent` / `sanitizeToolCalls` / `sanitizeMessages` | 167 / 195 / 227 | 已含 `reasoning_content` + `reasoning` + `tool_calls`（比上游基线还多注释） |

**与上游当前形态的结构差异 = 只差三块**（上游多出来的就这些）：

| 上游新增 | 本仓动作 | 难度 |
|---|---|---|
| `sanitizeFeatures` 加 `"Tokens prefixed with @"` | **直接加一行表项** | 无 |
| `sanitizeDshPathsRe` / `sanitizeDshPathsProbe` / `sanitizeDshPathsReplacement` | **新增 3 个包级符号** + `sanitizeTextWith` 里加 4 行 | 极小 |
| `sanitizeIdentitySentenceRe` / `…Replacement` / `sanitizeIdentityVerbs` | **新增 3 个包级符号** + `sanitizeTextWith` 的 `if brand` 块加 2 行（首行） + `hasIdentityFingerprint` 加 6 行 | 极小 |

`hasFingerprint` / `sanitizeContent` / `sanitizeToolCalls` / `sanitizeMessages` **完全不用动**（@@ 段的预检走 `sanitizeFeatures`，身份句走 `hasIdentityFingerprint`）。

### 6.2 本仓 prompt.go 现状（220 行）

`Load:33` / `Rewrite:51` / `Append:111` / `EnsureSystemFirst:173` / `const Degraded:25`；imports 只有 `_ "embed"`, `encoding/json`, `fmt`, `os` —— **缺 `strings`**（`SplitSystemMessages`/`MoveSystemToUser`/`splitSentences` 都要用）。缺三个函数：`SplitSystemMessages`、`splitSentences`、`MoveSystemToUser`。

### 6.3 `sanitize_brand_scope` 本仓已有的程度

| 项 | 本仓 | 上游 |
|---|---|---|
| 配置字段 | `features.sanitize_brand_scope`（`cmd/server/config.go:177`，默认 `"all"` 在 `:334`） | 同 |
| 归一化 | `config.go:530-534`：非 `identity` 一律回落 `all` | 同 |
| 运行时表示 | `Client.SanitizeBrandAll atomic.Bool`（`client.go:785`，默认 true `:859`） | `Client.SanitizeBrandScope atomic.Value(string)` + `brandScopeGlobal()` |
| 透传 | `client.go:954` → `PrepareBodyOptWithEffortsDefaultAndCapsScope(..., brandAll bool, ...)` → `sanitizeMessages(msgs, brandGlobal)` | `prepareBodyOpt(..., brandGlobal, ...)` |
| 作用范围 | `sanitizeMessages`：`brand := brandGlobal \|\| identity`（identity = role 去空格等值 `system`/`developer`） | **同**（逐字同形） |

**结论：作用范围与上游一致**（`all`=所有角色；`identity`=仅 system/developer），只是内部表示是 bool 而非字符串枚举。所以 §2 的身份句式一旦加上，自动受同一开关约束，**不需要新增配置项**。
唯一移植坑：`probe_channel.go` 里的 `c.SanitizeBrandScope.Store("all")` 在本仓要写成 `c.SanitizeBrandAll.Store(true)`；`TestBrandScopeDefaultGlobal` 那个用例（测 `brandScopeGlobal()`）本仓没有对应方法，**不要照搬**。

### 6.4 本仓 payload.go 现状（`enum 补 type` 的落点）

本仓 `normalizeToolPatterns`（`payload.go:575`）+ `unescapePatternLiteralEscapes`（`payload.go:596`）**与上游 `49f9db4` 前形态逐字一致** → 改名 `normalizeSchemaTree`（2 处调用点 + 2 处递归）与插入 enum 块是**纯机械照搬**；`inferEnumType` 是新增函数，需要新 import `math`（本仓 payload.go 当前 imports：`encoding/json` / `log` / `strings`，加 `math` 即可）。

---

## 7. 本仓 `prepareBody` / `prepareBodyOpt` 调用点清单

本仓**没有** `prepareBodyOpt`（小写）：管线被拆成一串**导出的向后兼容包装**，真正的实现是 `PrepareBodyOptWithEffortsDefaultAndCapsScope`。这既是「上游 #104 改签名漏更新调用点」教训的对照点，也说明本仓**只要改一个函数体 + 一个生产调用点**就够。

### 7.1 定义（`internal/upstream/payload.go` / `client.go`）

| 行 | 符号 | 签名要点 |
|---|---|---|
| `payload.go:107` | `PrepareBodyOpt(src []byte, sanitize bool) []byte` | → `…WithEffortsAndDefault` |
| `payload.go:116` | `PrepareBodyOptWithEfforts(src, sanitize bool, efforts)` | → `…AndDefault` |
| `payload.go:125` | `PrepareBodyOptWithEffortsAndDefault(src, sanitize, efforts, defaultEfforts)` | → `…DefaultAndCaps` |
| `payload.go:135` | `PrepareBodyOptWithEffortsDefaultAndCaps(src, sanitize, efforts, defaultEfforts, maxOut)` | → `…CapsScope`（brandAll=true 硬编码） |
| `payload.go:142` | `PrepareBodyOptWithEffortsDefaultAndCapsScope(src, sanitize, brandAll bool, efforts, defaultEfforts, maxOut)` | **真正的实现**（21 步管线，含 `normalizeToolPatterns` / `normalizeRoles` / `sanitizeMessages`） |
| `client.go:945` | `func (c *Client) prepareBody(body []byte, realm, uid, conversationID, convReqID string) []byte` | 生产入口 |

### 7.2 生产调用点（只有 2 处）

| 位置 | 调用 |
|---|---|
| `internal/upstream/client.go:954` | `PrepareBodyOptWithEffortsDefaultAndCapsScope(body, c.SanitizeFingerprints.Load(), c.SanitizeBrandAll.Load(), efforts, defs, maxOut)` |
| `internal/upstream/client.go:1247` | `prepared := c.prepareBody(body, a.Realm(), a.UID, meta.ConversationID, meta.ConversationRequestID)`（在 `ChatStreamContext` 内） |

### 7.3 测试调用点（改签名时会一起炸的地方）

| 被调用者 | 测试调用点数量 | 文件 |
|---|---|---|
| `PrepareBodyOpt(...)` | **14** | `cache_key_test.go:144`、`payload_normalize_test.go:48,58,66,88,126`、`sanitize_test.go:213,233,248`、`sse_test.go:16,25,41,57,67` |
| `PrepareBodyOptWithEfforts(...)` | **37** | `backfill_test.go`(8)、`payload_test.go`(17)、`thinking_test.go`(11)、`tool_pairing_test.go:191` |
| `PrepareBodyOptWithEffortsAndDefault(...)` | **3** | `payload_effort_test.go:58,79`、`payload_maxtokens_test.go:49` |
| `PrepareBodyOptWithEffortsDefaultAndCaps(...)` | **5** | `payload_maxtokens_test.go:17,25,33,41,61` |
| `PrepareBodyOptWithEffortsDefaultAndCapsScope(...)` | **0** | —（无测试直接调它） |

（数量为 `git grep --fixed-strings` 命中行数，含定义行本身在 `payload_test.go:184` 这类 helper 内的调用。）

**给本任务的直接结论**：本仓若要引入新参数（例如上游 `prepareBodyOpt` 的 `realm`/`policy`/`cache`/`injectCache`），只需改 `…CapsScope` 一个函数体 + 新建一个更长的包装，**14+37+3+5 = 59 处测试调用点全部不受影响**（它们调的是短签名包装）。这与上游把实现做成小写单函数的形态相反——上游每次加参数都要动测试（`4a577fc` 就是这么炸的：`#104` 给 `prepareBodyOpt` 加了 `realm`/`policy`，`db31fc2` 新增的 `TestPrepareBodyRewritesDshPaths` 用了 9 实参版本，测试文件不参与 `go build`，只有 `go vet`/`go test` 才暴露）。
`4a577fc` 的内容就是一行：`prepareBodyOpt(body, true, true, nil, nil, nil, "", "", false)` → `prepareBodyOpt(body, true, true, nil, nil, nil, "", nil, "", "", false)`（补 `nil` 的 `policy`）。**本仓无需移植**（没有这个函数）。

---

## 8. 本仓移植清单

### 8.1 必须改（P0，纯机械，风险极低）

1. `internal/upstream/sanitize.go`
   - `sanitizeFeatures` 表尾加 `"Tokens prefixed with @"`（行 15 的表）。
   - 新增 `sanitizeDshPathsRe` / `sanitizeDshPathsProbe` / `sanitizeDshPathsReplacement`（§1.1 原文，**`project root` 版**），放在 `sanitizeKvRe`（行 46）之后、`sanitizeRewrites`（行 57）之前——与上游顺序一致即可。
   - `sanitizeTextWith` 在 `if brand` 块**之后**、`if sanitizeHdrRe.MatchString`（行 124）**之前**插入 4 行 @ 段改写。
   - `if brand` 块**首行**插入身份句式 `ReplaceAllString`（在 `sanitizeIdentityRewrites` 循环之前，行 119-123 之间）。
   - 新增 `sanitizeIdentitySentenceRe` / `sanitizeIdentitySentenceReplacement` / `sanitizeIdentityVerbs`（§2.1/§2.2/§2.3 原文）。
   - `hasIdentityFingerprint`（行 155）加动词快路（§2.5，`strings.ToLower` + `Contains` + **正则复核**）。
2. `internal/upstream/payload.go`：`unescapePatternLiteralEscapes` → `normalizeSchemaTree`（2 处调用 + 2 处递归自查），插入 enum 补 type 块（§3.2），新增 `inferEnumType`（§3.1），imports 加 `math`。
3. `internal/prompt/prompt.go`：新增 `SplitSystemMessages`（§4.1 原文）、`splitSentences`（§4.1 原文）、`MoveSystemToUser`（§4.2 原文），imports 加 `strings`。
4. `internal/server/handler.go`（`chatCompletions`）：
   - 行 1089 附近：加 `splitApplied` / `movedApplied` 两个标志；把降级期 `Rewrite(Degraded)` 改成「拆句 → 搬家 → 替换」链（§4.5a）。
   - 行 1356-1369：在既有 `degradedApplied` 分支**之前**插入拆句与搬家两级（§4.5b），日志串照抄两条新串。
5. 测试：照搬上游用例（§8.4 清单）。上游 `prepareBodyOpt(...)` 11 实参调用在本仓要改成 `PrepareBodyOpt(body, true)` 或 `PrepareBodyOptWithEffortsDefaultAndCapsScope(body, true, true, nil, nil, nil)`。

### 8.2 需决策（有取舍，先定口径再动）

1. **身份句式的作用域**：默认 `sanitize_brand_scope=all` 时，**用户消息**里的 `You are a X powered by Y.` 也会被换成中性句。上游认可这个口径（`TestBrandScopeAllRewritesUserMessages` 同哲学），本仓若担心「改写用户原文」，可选：把身份句式限定到 `identity` 才应用（偏离上游）、或维持 `all`。**建议维持上游 `all`**，理由：11-128 是按原文匹配、不分角色。
2. **`SplitSystemMessages` 把 `developer` 拆成 `system`**：上游实现里拆出的每条都是 `role:"system"`（`prompt.go:150`），若原消息是 `developer`，拆分即提前做了 developer→system 归一（本仓 `payload.go:421 normalizeRoles` 稍后也会做，等价）。**是否保留 developer 角色**是口径选择；建议照上游（拆出 system），因为上游 role 白名单不含 developer。
3. **拆句后 `EnsureSystemFirst` 的次序**：本仓 `handler.go:1102` 在 prompt 模式块之后调用 `EnsureSystemFirst(body, h.cfg.PromptText)`。拆句/搬家都保持 `messages[0]` 是 system，因此在 1102 之前或之后做都安全；但**重试阶梯（行 1356+）拿到的是已过 `EnsureSystemFirst` 的 body**，三层抢救里只有 `MoveSystemToUser`/`Rewrite` 会重写首条内容——`Rewrite` 会**删掉所有 system 再插一条**，若客户端原本多云 system，`EnsureSystemFirst` 结果被吸收（与上游一致，无需额外处理）。
4. **@ 段替换串版本**：`project root`（上游现态，安全交集）vs `workspace root`（hub 回放原版）。**建议 `project root`**；若本仓已上线过 `workspace root` 且线上 200，则保持不回改也行（两者都能过，`project root` 多一层保险）。
5. **是否移植 `internal/probe/probe_channel.go`**：建议**不移植**（§5.4）。若移植，必须改 `SanitizeBrandScope.Store("all")` → `SanitizeBrandAll.Store(true)`。
6. **`sanitize_extra_features` 配置项**（上游 `4f79fc8` 明确**未取**）：不移植。

### 8.3 不要照搬（本仓形态不同 / 风险高）

1. `prepareBodyOpt` 相关的一切（本仓无此函数；`4a577fc` 整个 commit 可跳过）。
2. 上游 `internal/pool` 的 11102 TTL 改动（`4f79fc8` 附带的 `cooldown.go` / `entry.go`）——**与本任务三项无关**，但本仓当前仍是 `modelBlockBaseTTL = 6 * time.Hour` / `modelBlockMaxTTL = 24 * time.Hour`（`internal/pool/cooldown.go:346-348`），若后续要一并吸收，改 2 个常量 + 2 处注释 + 常量守卫用例即可。
3. 上游 `TestBrandScopeDefaultGlobal`（依赖 `brandScopeGlobal()` 方法，本仓是 `SanitizeBrandAll` bool）。
4. 上游 `db31fc2`/`4f79fc8` 里对 `sanitize_test.go` 的 `prepareBodyOpt(body, true, true, nil, nil, nil, "", nil, "", "", false)` 调用形态——本仓用 `PrepareBodyOpt(body, true)`。
5. 上游 `handler.go` 的行号（本仓 `chatCompletions` 在 839，上游在 774；且本仓行尾是 **CRLF**，`git diff`/patch 会显得巨大——**别信行数差，按函数名 + 注释串定位**）。

### 8.4 建议一并移植的用例清单

`internal/upstream`：
- `TestDshPathsParagraphRewritten`、`TestDshPathsOlderWordingRewritten`、`TestDshPathsMultilineRewritten`、`TestDshPathsEmbeddedKeepsSurroundings`、`TestDshPathsIdempotent`、`TestDshPathsUnrelatedAtTextUntouched`、`TestPrepareBodyRewritesDshPaths`（7 条，`db31fc2`）
- `TestIdentitySentenceVariants`、`TestIdentitySentenceFalsePositives`、`TestIdentitySentenceScope`、`TestDeepSeekHarnessBrandRewritten`（上游新版是**整句断言**：输入 `"You are an AI agent powered by " + brand + "."`、输出 `HasPrefix(out, "You are a helpful AI assistant.")`；本仓无此用例，属**新增**）、`TestPrepareBodyRewritesDeepSeekHarness`
- `TestToolSchemaEnumTypeInflated`、`TestToolSchemaEnumTypeNested`、`TestToolSchemaEnumTypeLeavesResponseFormat`（`tool_enum_type_test.go`）
- 测试夹具常量 `dshPathsNow` / `dshPathsOld`（§1.2 原文，直接复用）

`internal/prompt`（新文件 `split_test.go` / `move_test.go`）：`TestSplitSystemMessagesKeepsTextInSystem`、`TestSplitSystemMessagesBailsOnSingleSentence`、`TestSplitSystemMessagesOnlyLongest`、`TestSplitSentencesIgnoresNonSentenceDots`、`TestMoveSystemToUserKeepsText`、`TestMoveSystemToUserDeveloper`、`TestMoveSystemToUserBails`、`TestMoveSystemToUserNeedsNeutral`。

`internal/server`（`handler_test.go`）：`TestContentBlockedSplitSystemFirst`（多句 system → 2 次上游调用 → 第二次是 2 条 system + 1 条 user）、以及原有 `TestContentBlockedTriggersDegradedRetry`（单句 → 搬家，保持通过）。

---

## Caveats / 未确认

- `e4dd458` 的 A–G 表与 `probe_channel.go` 的用例**不完全一一对应**（探针有 A/B/C/D/E/F/F2/G，表里没写 B 组另列…实际 B 就是探针的 B）：以探针代码为准，§4.4 表格已按探针 8 变体对齐；表里 `F2` 是探针的 `F2`。
- 上游 A–G 是**单次实验**（D/E/G 各 3 采样），不是统计显著性实验；「拆句能过」的因果解释是「上游按连续原文匹配」，属推断，但 F/F2 两组 200 + A 组 400 的对照已经足够。
- **本仓现有用例不会被身份句式改动打破（已逐条核对）**：`git grep -E "powered by|built by|built on|created by|developed by|made by|trained by"` 在本仓 `*.go` 里**零命中**（生产代码与测试都没有这七种搭配）。具体到三条品牌用例：
  - `sanitize_brand_test.go:9 TestSanitizeBrandIdentityRewrite` 的 `"You are DeepSeek Harness, a terminal coding agent."` **不匹配**新正则（`DeepSeek Harness` 后是逗号 + `a terminal coding agent`，没有动词搭配）→ 仍走品牌词改写，断言 `contains DeepSeekRuntime && !contains "DeepSeek Harness"` 继续成立。
  - `:21 TestSanitizeBrandScopeIdentityOnly` 的 `"You are DeepSeek Harness."` 同样不匹配（`[\w.-]+` 把 `Harness.` 连点一起吃，后面没有 `\s+`/动词）→ 只剩品牌改写，断言 `contains DeepSeekRuntime` 成立。
  - `:38 TestSanitizeBrandScopeAll` 的 `"DeepSeek Harness"` 无 `You are` 前缀 → 只走品牌改写，断言 `== "DeepSeekRuntime"` 成立。
  - 上游把 `TestDeepSeekHarnessBrandRewritten`（`upstream/main:sanitize_test.go:394`）改成了**整句断言**（`HasPrefix(out, "You are a helpful AI assistant.")`），因为它的输入是 `"You are an AI agent powered by " + brand + "."`——**本仓没有这条用例**，移植时是新增，不是改旧。
- 上游 `TestBrandScopeDefaultGlobal`（`upstream/main:sanitize_test.go:565`）依赖 `brandScopeGlobal()`，本仓无此方法 → 若要测默认作用域，改断言 `c.SanitizeBrandAll.Load() == true`。
- 本仓 Go 文件普遍 **CRLF**（`handler.go` 2283 行全 CRLF、`sanitize.go` 271 行全 CRLF、`prompt.go` 220 行全 CRLF、`payload.go` 624 行全 CRLF），上游是 LF。用 `git diff 5367e78 -- <file>` 会看到整文件重写，**别据此判断本仓「改动很大」**——实际差异只有注释措辞/行尾。
