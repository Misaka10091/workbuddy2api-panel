# PRD：同步上游 P0（出站契约 5.9.3 / 11-128 抢救 / 6020-6021 排队）

## 背景

上游 `misakano7545/workbuddy2api-panel` 在 `5367e78`（tag `v2026.10.27.1727`，本仓上次同步基线）之后又推进了 16 个提交，其中三项直接关联**禁号风险**、**现网必现的 400** 与**把可恢复错误变成致命错误**。

本仓是手工挑选同步的分叉（分叉点 `dbd7c68`），不能 merge，只能逐项按上游语义移植并适配本仓结构。分叉点之后本仓已有大量本地改动（realm 分离 CN/global、API 密钥按域隔离、模型白名单、首帧否决、模型锁三态、realm 级池判断），**上游 diff 不能照抄**。

关键前提：这次上游有三处结论**反转了我们第一轮同步的实现**——
- 第一轮按当时口径把 `X-Private-Data` 写死 `true`，上游 5.9.3 真机抓包证明默认态是 `false`（企业版才 `true`）；
- 第一轮给 `CommonHeaders` 加了 `X-Machine-ID` / `X-Session-ID`，真机与 app.asar 全量字符串证明客户端**从不发**这两个头；
- 第一轮让 RPC 都带 `Origin`/`Referer` + 全出站 `X-CodeBuddy-Request`，真机证明桌面端 RPC 层是 node fetch，既没有 `Origin`/`Referer` 语义，`X-CodeBuddy-Request` 也只出现在 chat。

也就是说，本仓当前的出站报文在「多发客户端从不发的头」这个方向上比同步前更不自然。本轮必须先纠正，再谈新增。

## 目标

1. 出站请求契约对齐 WorkBuddy 5.9.3 / CLI 2.165.0 真机形态：头键集、取值、body 编码、ID 形态、端点选择全部以客户端为准。
2. 消除本仓唯一漏掉的 DSH 内容指纹（`@` 路径说明段）与身份句式骨架，恢复 11-128 的自愈能力，并把「绑架 system」的粗暴抢救换成三级递进。
3. 6020/6021 渠道排队不再以 503 终结请求：恢复请求内轮转，末端回 429 + `Retry-After`。
4. 收敛 11102 负缓存 TTL，避免一次分类误判把 `(账号, 模型)` 锁死 6 小时、轮一圈把整池该模型打成 503。

## 非目标（本轮不做）

- **思考档位策略**（上游 `1792ef1`/`b3623b5`/`e3a6f7a`）：新增 `thinking` 配置面 + `/v1/models` 声明 + 面板三个控件 + 用量总积分卡，是纯新增功能，与本轮风险修复混在一起会污染回归面。单独立项。
- **上游目录监控 watchdog**（上游 `49f9db4` 第 4 项）：新增配置面 + 定时器 + webhook 通知，独立功能，单列。
- **默认监听改回环**（上游 `49f9db4` 第 3 项）：一行改动但影响部署形态（容器必须显式覆盖），需与发布/文档一起动，单列。
- **`internal/probe/probe_channel.go` 真机探针**：上游用于真机 A/B 实测的独立探针，会打真实上游，不进本仓主线（除非实现时确认为 build tag 隔离的离线工具，见 design）。
- **国际版（global）版本号跟版**：上游只抓了 CN 5.9.3 的真机包，global 侧无对应抓包证据。本轮 global 保持 `5.8.4` 不动，避免用无证据的值制造新的形态偏差。
- **上游 `#310`/`#311`/`#315` 等依赖 `/console` 回退路径或渠道记忆的 PR**：本仓无该前置条件。

## 需求

### 改动集 ①：出站契约对齐 5.9.3（P0）

**R1-1 版本常量跟版到 CN 5.9.3 / CLI 2.165.0。**
UA 拼装的三条路径（`userAgent` / `v3ProbeUA` / `desktopUAFor`）在 CN 下输出均以 5.9.3 + `CLI/2.165.0` 为准，形态（三段重复品牌 / 两段单品牌）不变。global 版本号保持不变。`config.user_agent` / `cli_version` 覆盖机制与热生效语义不变。

**R1-2 纠正第一轮的三处「多发头」。**
- `X-Machine-ID` / `X-Session-ID`：从 `CommonHeaders` 整层移除（不只是 chat 路径删）；设备标识只走 `/v2/report` body。
- `Origin` / `Referer`：桌面端 RPC 一律不注入；`originRefererCN` 若仍有使用点，取值改为 `https://www.workbuddy.cn`（`www.codebuddy.cn` 只是 billing API 主机名，不是 web origin）。
- `X-CodeBuddy-Request`：只在 `ChatHeaders` 注入，refresh / busy / report / v3-config / billing 不再带。

**R1-3 `Accept-Language` 整层移除。**
`CommonHeaders` 与 `BillingHeaders` 都不再注入（桌面端 RPC 无该头）。`acceptLanguageFor` 及其 realm 分支按实际情况删除或降为未使用，不留半挂状态。

**R1-4 `X-Private-Data` 改为条件取值。**
个人号 `false`（客户端默认开模型优化），企业版（默认关模型优化）`true`；判据来自账号本身已有的字段，不新增配置项。

**R1-5 补齐 client 侧常量头。**
chat 出站补 `X-Agent-Runtime: local-agent`；补 `baggage`（OTel W3C 形态，四键，两个 buddy 键恒空但保留 `k=` 形态，两个 `codebuddy.*` 键与同请求的 `X-Conversation-ID` / `X-Conversation-Request-ID` 同值）。头间取值必须自洽——不一致本身就是可判别形态。

**R1-6 ID 形态对齐 UUIDv7。**
`X-Conversation-ID` 与 `acp-connection-id` 的零值兜底改为时间有序 UUIDv7（同 `(账号, 会话, purpose)` 恒同值、解出合理时刻、跨请求可排序）。`X-Request-ID` 保持纯随机 32 hex。`prompt_cache_key` 仍用稳定 v4，槽位不漂移。`X-Trace-ID` 不得等于 `X-Conversation-Request-ID`。

**R1-7 refresh 请求补齐。**
真机 11 头 + body `{}`：补 `Authorization`（**与 `X-Refresh-Token` 双发**）、`X-Request-ID`、`X-User-Id`、`X-Domain`；`Accept` 用宽松值；body 为 2 字节 `{}`，不再是空 body。注意与 chat 相反——chat 绝不带 `X-Refresh-Token`。

**R1-8 `/v2/chat/busy` 轻头集（本仓不适用，仅记录）。**
上游把该路径从"复用整个 chat 重头族"收成 8 个轻头。本仓无该路径（`grep 'chat/busy'` 为空），本轮不新增 `ModelBusy` 客户端函数、不建对应黄金集 case。若将来补齐该路径，再按上游 8 头形态实现。

**R1-9 chat body 按大小 gzip。**
超过阈值（上游取 1024 字节）时压缩并带 `Content-Encoding: gzip`，极小球体不压（压了更长）；`Accept-Encoding` 声明与客户端一致。阈值与压缩级别不改变可解性。

**R1-10 `/v2/report` 对齐。**
通道改走 chat 域（本仓 CN 为 `www.workbuddy.cn`）、UA 改三段式、字段集按 5.9.3 增删（新增 `isMaxMode` / `source` / `inputAttemptId` / `vcsType` / `vcsRepo` / `vcsBranchName` / `vcsRevId` / `traceId` 与设备指纹；`agentName`/`agentType` 改 `cli`/`main`；删 5.9.3 不存在的字段）。CN billing 域同时从 `codebuddy.cn` 改 `www.workbuddy.cn`，并留 `cn.billing_base` 覆盖逃生门（空 = 新默认）。

**R1-11 条件头按维度触发。**
`X-Id-Source` / `X-Userinfo` 的 `id_source` 段按账号 JWT 声明条件触发（不只企业号）；`X-Root-Request-ID` 改会话级根（无根时回落轮级值，头不能丢）；`X-Agent-Type` 分 `main` / `auxiliary`，网关自发的辅助链（夜猫子对话、面板判据对话、工具探测）报 `auxiliary`。

**R1-12 黄金头集回归。**
把 chat / report / refresh / v3-config / billing 五条路径（本仓无 busy）的真机出站头**键集**固化为本仓测试（集合精确相等，多发/漏发都要红），并对关键值与「绝不应出现」的头单独断言。

### 改动集 ②：11-128 内容审核抢救（P0）

**R2-1 中和 DSH 系统提示里的 `@` 路径说明段。**
按本仓既有做法补一条：字面量预检 + 整段正则 + 同义改写。语义全保留（`@` 路径含义、相对路径基准、结尾斜杠表示目录、`@"..."` 表示含空格路径），只换措辞让连续原文特征失配——不删段落。覆盖上游点名的两种线上措辞。段外 `@`（邮箱、npm 包名、Windows 路径）不受影响；幂等。

**R2-2 身份句式整句中性化。**
现有规则只换品牌词，`You are … powered by …` 的句子骨架原样保留——骨架本身也是特征。新增句式级中性化，覆盖上游点名的动词搭配集合，修饰词窗口有界以免误伤「You are free to use tools built by …」这类分词修饰宾语的句子。预检加动词字面量快路，命中后再交正则确认，避免把讲解产品的普通消息拖进清洗。

**R2-3 `@` 段替换串改为 `project root`。**
上游两处证据冲突（一说上游已把 `workspace root` 列黑、一说整段改写后 `workspace root` 保留仍 200）。`project root` 在两种说法下都安全且与 DSH 语义等价，取它消掉不确定性。

**R2-4 工具 schema 的 `enum` 缺 `type` 时补推断类型。**
Kimi 系对参数 schema 严格校验，属性只写 `enum` 不写 `type` 直接 400 `code=11133`，而 DSH 等客户端的整套工具定义里就有这种写法——该模型对这些客户端等于全废。类型能无损推断就补（全串 string / 全整数 integer / 含小数 number / 全布尔 boolean），推断不出唯一类型（混合标量、含 null）保持原样让上游按原语义报错，不猜。`enum` 取值一字不改；`response_format` 不走这条（那是输出约束，补错类型会改语义）。不新增遍历，塞进既有 schema 遍历里。

**R2-5 11-128 抢救改三级递进，拆句优先。**
真机实测结论：`system` 里只要把连续原文断开就能过；「搬家」不是保住人格的必要手段；短 system 拆开反而废掉指令。因此顺序改为：
1. **按句界拆最长的那条 system**（文本一字不改、承载仍是 system、指令优先级不变；只拆最长的一条）；
2. 拆不动（单句 system）才把 system 原文**搬进首条 user**（一个字符都不丢）；
3. 都不行才退回整体替换。

任一层成功即置降级态，后续新请求直接按该形态出站——否则每条请求都要先撞一次 400，等于双倍上游调用。

### 改动集 ③：6020/6021 排队与 11102 负缓存（P0/P1）

**R3-1 6020/6021 恢复请求内轮转。**
`ErrUpstreamBusy` 由 fail-fast 改为换号重试：**不冷却、不熔断、不喂连败计数**（2026-10-15 实案教训保留）。轮转次数受既有 `MaxRotate` 约束。

**R3-2 末端映射改 429 + `Retry-After`。**
轮转耗尽后回 429 而不是 503：`Retry-After` 优先取上游 body 里的等待口径（6020 的 `data.retry_after`），缺失时用有界缺省值，并受既有上限封顶；上游原文仍原样透传在 `message` 里（遵守本仓「`error.message` 不替换」硬约定）。

**R3-3 语义分界写清。**
6000（模型级限流，罚 `(账号, 模型)`）、6020/6021（渠道排队，不罚号但可换号）、其他 429 各自的处置必须在代码注释里写明，避免后续再次被「统一按限流处置」改回去。

**R3-4 11102 负缓存 TTL 收敛。**
`modelBlockBaseTTL` / `modelBlockMaxTTL` 从 6h/24h 收到 2m/10m 量级：11102 不总是「这号真没这模型」（路径级、上游瞬时抖动、分类误判都会给同一个码），原参数把一次误判放大成 6 小时锁死。指数退避保留（`Hits` 退避到新封顶），真没模型的号不会变成每请求重试。同步修正仍写 6h~24h 的注释，并加常量守卫用例防回归。

## 验收标准

1. `go build ./...`、`go vet ./...`、`go test ./...` 全绿（本机 `go` 在 `C:\tools\go\bin\go.exe`，需临时入 PATH；JS 测试需 `node`，缺 node 会 skip——skip 不等于通过，需在结论里写明）。
2. 改动集 ① 有**头键集回归测试**：五条路径（chat / report / refresh / v3-config / billing）的出站头集合精确相等断言；并断言 `X-Machine-ID`/`X-Session-ID`/`Accept-Language` 在任何路径都不出现、`X-CodeBuddy-Request` 只出现在 chat、`Origin`/`Referer` 不出现在 RPC。
3. 改动集 ② 的每条新规则都有「命中即改写」+「段外/句外不误伤」+「幂等」三类用例；`enum` 补 type 有「能推断」「推断不出保持原样」「`enum` 取值不变」用例。
4. 改动集 ③ 有「两个号各撞 6020 → 轮转 → 429 + Retry-After + 零惩罚」端到端用例、`data.retry_after` 透传用例、TTL 常量守卫用例；且**改动前的旧断言会被明确改写而不是删掉**。
5. 既有契约零漂移：OpenAI 错误体形状、`gateway_hint` 平行字段、SSE 白名单、`X-Conversation` 三件套、realm 隔离、模型白名单、首帧否决、模型锁三态对本仓既有测试零回归。
6. 三个改动集可独立提交、独立回滚；不引入需要重启才生效的配置项语义变更（`cn.billing_base` 若引入，需明确热生效还是加入 `restartRequiredFields`）。
7. 不向真实账号发请求：测试只打本地假上游。`internal/probe` 若移植，必须不与默认构建一起编译（或明确不进主线）。

## 风险

- **头级改动的不可逆性**：出站报文是禁号风险的主战场，改错方向要等下一轮真机抓包才能证伪。缓解：以黄金头集测试固化，且每条改动在 design 里写明证据来源（哪个提交、哪条抓包索引）。
- **gzip 改变上游看到的 body 形态**：若上游某些路径不做解压校验，可能引入新的失败面。缓解：阈值保持保守（极小 body 不压），并保留一键回退（配置或常量）。
- **6020/6021 轮转会放大上游调用量**：客户端按 `Retry-After` 重试的节奏下轮转次数有界，但高峰期的 `MaxRotate` 倍放大是真实成本。缓解：只在「排队间歇」受益，不改变排队本身；轮转次数沿用既有上限。
- **三级抢救的状态机**：降级态是进程级的，若某账号真的永久不支持 system 承载，会长期走第 2/3 层。缓解：降级只在本就会失败的请求上触发一次，且每层都留日志串便于线上统计。
- **`project root` 替换串**：依赖上游「换措辞即放行」的行为，若上游改成语义审核则整体失效。缓解：与既有「换一词即绕过」的假设同源，不额外增加风险面。
