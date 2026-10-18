// Package apikeys 多 API 密钥管理：每个密钥绑定一个账号域（国内版 / 国际版）。
//
// 背景：网关原本只有 config.api_key 一把密钥，它同时用于面板鉴权与 /v1/* 调用，
// 且请求走哪个域只能靠模型名前缀（"global:gpt-6-sol"）表达。混池部署时这带来两个
// 实际问题：
//
//  1. 拿不到「按域发密钥」——给国内版客户端一把、国际版客户端一把，各自只能
//     落到自己那批账号上，不能靠客户端自觉写前缀；
//  2. 一把密钥泄漏 = 全池暴露（两个域的账号一起失守），无法按域回收。
//
// 本包给出最小可用的密钥表：name（人类可读标签）/ key（明文比对）/ realm
// （cn|global|""=不限）/ enabled。域绑定是**强约束**：密钥绑了 global，用该密钥
// 请求裸模型名（无前缀）也只会落在 global 账号上；显式写了相反域的前缀则直接
// 拒绝（400），而不是静默改域——静默改域会让客户端以为自己在打国际版，
// 实际消耗国内版积分，是最难排查的一类计费错位。
//
// 设计约束：
//   - 与 config.api_key 并存而非替换：老部署的 config.api_key 继续有效（视为
//     不限域的主密钥），零迁移成本；密钥表为空时行为与历史完全一致。
//   - 密钥明文落盘（config 里 hand-write 的那种也是明文），但**只在面板接口
//     按需回显**（selector 决定是否带明文）；日志/状态接口一律不出现明文。
//   - 线程安全：读取路径（每请求）走 atomic 快照，写路径（面板）加锁重建快照。
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 域枚举。与 auth.Realm() / server.resolveModel 的取值完全一致。
const (
	RealmAny    = ""       // 不限域：密钥可用于任意账号（含混池）
	RealmCN     = "cn"     // 国内版
	RealmGlobal = "global" // 国际版
)

// ErrNotFound 密钥不存在。
var ErrNotFound = errors.New("api key not found")

// ErrDuplicate 密钥值重复（同一把密钥不能绑两个域，否则判定歧义）。
var ErrDuplicate = errors.New("api key already exists")

// Key 一把 API 密钥。
type Key struct {
	// Name 人类可读标签（面板展示用，可重复；不参与鉴权）。
	Name string `json:"name"`
	// Key 密钥明文。鉴权按明文常量时间比对（见 Verify）。
	Key string `json:"key"`
	// Realm 绑定域："cn" / "global" / ""（不限）。非法值在 Set 时拒绝。
	Realm string `json:"realm"`
	// Disabled 停用：保留记录但拒绝鉴权（比删除更安全——删了客户端只会拿到
	// 401 且无从追溯这把密钥原本绑哪个域）。
	Disabled bool `json:"disabled,omitempty"`
	// CreatedAt 创建时刻（面板展示/审计）。
	CreatedAt time.Time `json:"created_at,omitempty"`
	// Note 备注（可选）。
	Note string `json:"note,omitempty"`

	// AllowModels / DenyModels 模型过滤（每把密钥独立）。
	//
	// 语义（与业界网关的常见约定一致）：
	//   - AllowModels 为空 = 不限制（该密钥可用全部本域模型）；非空 = 白名单，
	//     只有命中的模型可用。
	//   - DenyModels 优先于 AllowModels：命中黑名单即拒绝，无论是否在白名单。
	//   - 两者都为空 = 不做任何过滤（历史行为，零回归）。
	//
	// 匹配对象是**模型 ID**（请求体 model 字段 / 目录条目的 id），不是展示名称。
	// 例：目录里 id="glm-5.3"、name="[国内版] GLM-5.3"——规则要写 glm-5.3，
	// 写 "GLM-5.3" 或 "[国内版] GLM-5.3" 都匹配不上（name 是给人看的，随时可能
	// 因上游改文案而变；id 才是调用契约）。这条边界必须钉死，否则运维按界面上
	// 看到的名称写规则，会得到永久不匹配的静默失效。
	//
	// 元素支持通配符（glob 风格，大小写不敏感）：
	//   *  匹配任意长度（含空）      glm-*     匹配 glm-5.2 / glm-5.3-flash
	//   ?  匹配单个字符              gpt-?.?-sol
	//   无通配符 = 精确匹配          hy3       只匹配 hy3
	//
	// 判定前会剥掉 "cn:"/"global:" 前缀（密钥已绑域，前缀在该密钥视角下是噪音；
	// 用户照抄 /v1/models 的 id 也不该因此匹配不上）。
	AllowModels []string `json:"allow_models,omitempty"`
	DenyModels  []string `json:"deny_models,omitempty"`

	// filter 编译后的过滤规则（buildSnapshot 时填充，只读共享）。
	// 未导出：不参与 JSON 序列化，也不该被外部构造——保证配置里写的永远是
	// 原始的 pattern 列表，而不是内部派生形态。
	filter *ModelFilter
}

// ModelFilter 编译后的模型过滤规则。零值表示「不限制」。
//
// 预编译的原因：判定发生在每个 /v1/models（逐条）与每次 chat 请求上，
// 反复解析 pattern 列表是纯浪费；且编译期能一次性剔除空串/纯空白条目。
type ModelFilter struct {
	allow []string // 已归一化的 pattern（小写、去域前缀）
	deny  []string
}

// compileFilter 归一并编译 pattern 列表。
//
// 不在这里校验语法（返回值只有 filter）：非法 pattern 的挑拣放在 Normalize /
// Add 的校验路径上（ValidatePatterns），这样"能不能存"与"怎么匹配"两件事分开——
// 匹配函数永远不需要处理 error 分支。
func compileFilter(allow, deny []string) *ModelFilter {
	f := &ModelFilter{
		allow: normalizePatterns(allow),
		deny:  normalizePatterns(deny),
	}
	if len(f.allow) == 0 && len(f.deny) == 0 {
		return nil
	}
	return f
}

// normalizePatterns 去空白、去域前缀、转小写（通配符匹配大小写不敏感）。
func normalizePatterns(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		// 用户可能照抄 /v1/models 的 "cn:glm-5.2"：剥掉域前缀再匹配裸名。
		p = strings.TrimPrefix(p, "cn:")
		p = strings.TrimPrefix(p, "global:")
		out = append(out, p)
	}
	return out
}

// Active 报告该过滤是否真的生效（空规则 = 不限制）。
func (f *ModelFilter) Active() bool {
	return f != nil && (len(f.allow) > 0 || len(f.deny) > 0)
}

// Allows 报告裸模型名是否被允许。filter 为 nil 时恒 true（不限制）。
//
// 判定顺序：先黑名单（命中即拒），再白名单（非空则必须命中）。
// 这个顺序让「白名单 glm-* + 黑名单 glm-5.1」这种写法能表达「除某一版之外的
// 整个系列」——是运维最常用的组合。
func (f *ModelFilter) Allows(model string) bool {
	if !f.Active() {
		return true
	}
	m := normalizeModelName(model)
	for _, p := range f.deny {
		if globMatch(p, m) {
			return false
		}
	}
	if len(f.allow) == 0 {
		return true
	}
	for _, p := range f.allow {
		if globMatch(p, m) {
			return true
		}
	}
	return false
}

// normalizeModelName 归一化模型名：去空白、转小写、剥域前缀。
// pattern 侧走 normalizePatterns，两侧用同一套归一化才不会「写的和判的不一致」。
func normalizeModelName(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "cn:")
	m = strings.TrimPrefix(m, "global:")
	return m
}

// Filter 返回该密钥编译后的过滤规则（nil = 不限制）。
func (k Key) Filter() *ModelFilter { return k.filter }

// ModelAllowed 便捷判定：该密钥是否允许某个模型。
func (k Key) ModelAllowed(model string) bool { return k.filter.Allows(model) }

// globMatch glob 风格匹配（**大小写不敏感**），支持 * 与 ?。
//
// 直接用标准库 path.Match 而不是自己写回溯匹配器：
//   - 语义完全够用。path.Match 的 '* 不跨 /' 约束在这里不成立——模型名不可能
//     含 '/'，所以分隔符差异被消掉了；
//   - 正确性有保证。手写版本实测踩过两个坑（'*' 吞空串、尾部 '*' 未消费），
//     都是靠测试才发现；标准库没有这类风险；
//   - 非法 pattern 返回 error。以前我声称"glob 没有非法形态"，其实 '[' 未闭合
//     就是非法（path.Match 会报 syntax error）。这里 error 当"不匹配"处理，
//     并在保存时就由 ValidatePatterns 把非法规则挑出来。
//
// 大小写归一化放在**本函数内**做，而不是交给调用方：它是对外承诺的语义，
// 两个调用点（ModelFilter.Allows 与测试）都直接依赖它。生产路径虽然已经
// 在 normalizePatterns/normalizeModelName 里转过小写，但重复一次 ToLower
// 是常数开销，换来的是"语义自洽"——不依赖调用方是否记得归一化。
func globMatch(pattern, s string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(s))
	return err == nil && ok
}

// ValidatePatterns 校验一组 pattern 的语法，返回带下标的错误列表。
//
// 供面板在**保存时**就拒绝非法规则（如未闭合的 '['）：存进去才报错的规则
// 等于静默失效——过滤器永远不匹配，而运维以为限制生效了。
func ValidatePatterns(pats []string) []error {
	var errs []error
	for i, p := range pats {
		norm := strings.ToLower(strings.TrimSpace(p))
		if norm == "" {
			continue
		}
		norm = strings.TrimPrefix(norm, "cn:")
		norm = strings.TrimPrefix(norm, "global:")
		if _, err := path.Match(norm, ""); err != nil {
			errs = append(errs, fmt.Errorf("pattern %d (%q): %w", i, p, err))
		}
	}
	return errs
}

// Clone 深拷贝（不含内部状态，字段都是值类型，直接赋值即可）。
func (k Key) Clone() Key { return k }

// RealmLabel 域的中文标签（日志/面板共用）。
func RealmLabel(realm string) string {
	switch realm {
	case RealmCN:
		return "国内版"
	case RealmGlobal:
		return "国际版"
	default:
		return "不限域"
	}
}

// NormalizeRealm 归一化外部传入的域：只接受 cn / global / ""（不限），
// 其余一律报错而不是回落——密钥的域绑定是计费隔离的边界，拼错必须显式失败。
func NormalizeRealm(realm string) (string, error) {
	switch v := strings.TrimSpace(realm); v {
	case "", RealmCN, RealmGlobal:
		return v, nil
	case "CN", "cn ":
		// 大小写/空白容错（人工填配置的常见形态），归一后仍是精确枚举。
		return RealmCN, nil
	default:
		if strings.EqualFold(v, RealmGlobal) {
			return RealmGlobal, nil
		}
		if strings.EqualFold(v, RealmCN) {
			return RealmCN, nil
		}
		return "", fmt.Errorf("realm must be cn/global/empty, got %q", realm)
	}
}

// ValidateKey 校验密钥值本身：非空、无空白（HTTP 头里不能带空格）、长度合理。
func ValidateKey(k string) error {
	if k == "" {
		return errors.New("key must not be empty")
	}
	if strings.TrimSpace(k) != k {
		return errors.New("key must not contain leading/trailing whitespace")
	}
	if strings.ContainsAny(k, " \t\r\n") {
		return errors.New("key must not contain whitespace")
	}
	if len(k) < 8 {
		return errors.New("key too short (min 8 chars)")
	}
	if len(k) > 256 {
		return errors.New("key too long (max 256 chars)")
	}
	return nil
}

// Generate 生成一把随机密钥（32 字节 hex，带 wb- 前缀便于肉眼识别）。
func Generate() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return "wb-" + hex.EncodeToString(b), nil
}

// snapshot 不可变读快照（每请求读取，避免持锁）。
type snapshot struct {
	// byKey 明文密钥 → Key（含已停用的条目；停用判定在 Verify 里做，
	// 为的是能区分「密钥不存在」与「密钥已停用」两种 401 原因）。
	byKey map[string]Key
	// list 按创建时刻 + Name 排序的稳定列表（面板展示）。
	list []Key
}

// Store 密钥表（并发安全的原子持有者）。
type Store struct {
	mu sync.Mutex // 保护写路径（Set/Remove 重建快照）
	p  atomic.Pointer[snapshot]
}

// New 以初始密钥列表构建。
//
// 列表中的非法条目（空 key / 非法 realm / 重复 key）在这里**跳过而不是**让整个
// 进程起不来：密钥表是运维数据，一条脏数据不该导致网关拒绝启动（那会让正在
// 服务的流量全断）。跳过的条目由调用方记 WARN 日志。
func New(keys []Key) (*Store, []error) {
	s := &Store{}
	normalized, errs := Normalize(keys)
	s.p.Store(buildSnapshot(normalized))
	return s, errs
}

// Normalize 归一化并去重：返回可用的密钥列表 + 每条被丢弃条目的原因。
// 保持输入顺序（面板上「后加的在下」符合直觉）。
func Normalize(keys []Key) ([]Key, []error) {
	var (
		out  []Key
		errs []error
		seen = map[string]bool{}
	)
	for i, k := range keys {
		realm, err := NormalizeRealm(k.Realm)
		if err != nil {
			errs = append(errs, fmt.Errorf("api_keys[%d] (%s): %w", i, k.Name, err))
			continue
		}
		if err := ValidateKey(k.Key); err != nil {
			errs = append(errs, fmt.Errorf("api_keys[%d] (%s): %w", i, k.Name, err))
			continue
		}
		if seen[k.Key] {
			errs = append(errs, fmt.Errorf("api_keys[%d] (%s): %w", i, k.Name, ErrDuplicate))
			continue
		}
		seen[k.Key] = true
		k.Realm = realm
		k.Name = strings.TrimSpace(k.Name)
		if k.Name == "" {
			k.Name = RealmLabel(realm) + "密钥"
		}
		if k.CreatedAt.IsZero() {
			k.CreatedAt = time.Now()
		}
		// 模型过滤：保留用户原始 pattern（落盘可读），另编译一份供热路径判定。
		// 先校验语法：非法 pattern（如未闭合的 '['）会让该条规则**永远不匹配**，
		// 存进去等于静默失效（运维以为限制生效、实际全放行）。此处直接拒掉整条密钥，
		// 与其它校验失败同一处理方式（跳过 + 报错），由调用方记 WARN。
		if patErrs := append(ValidatePatterns(k.AllowModels), ValidatePatterns(k.DenyModels)...); len(patErrs) > 0 {
			for _, pe := range patErrs {
				errs = append(errs, fmt.Errorf("api_keys[%d] (%s): %w", i, k.Name, pe))
			}
			continue
		}
		k.AllowModels = normalizePatterns(k.AllowModels)
		k.DenyModels = normalizePatterns(k.DenyModels)
		k.filter = compileFilter(k.AllowModels, k.DenyModels)
		out = append(out, k)
	}
	return out, errs
}

// buildSnapshot 由列表构建读快照（调用方负责串行化）。
func buildSnapshot(keys []Key) *snapshot {
	s := &snapshot{
		byKey: make(map[string]Key, len(keys)),
		list:  make([]Key, len(keys)),
	}
	copy(s.list, keys)
	for _, k := range keys {
		s.byKey[k.Key] = k
	}
	sort.SliceStable(s.list, func(i, j int) bool {
		if !s.list[i].CreatedAt.Equal(s.list[j].CreatedAt) {
			return s.list[i].CreatedAt.Before(s.list[j].CreatedAt)
		}
		return s.list[i].Name < s.list[j].Name
	})
	return s
}

// List 返回当前密钥列表（副本，按创建时刻稳定排序）。
func (s *Store) List() []Key {
	if s == nil {
		return nil
	}
	snap := s.p.Load()
	if snap == nil {
		return nil
	}
	out := make([]Key, len(snap.list))
	copy(out, snap.list)
	return out
}

// Len 返回密钥数量（0 = 未启用多密钥，退回单一 config.api_key）。
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	snap := s.p.Load()
	if snap == nil {
		return 0
	}
	return len(snap.list)
}

// Lookup 按明文密钥查条目（区分「不存在」与「已停用」两种失败）。
//
// 返回值 found=true 但 key.Disabled=true 表示密钥存在但被停用——调用方据此
// 给出不同的错误文案（停用 vs 无效），便于运维自查。
func (s *Store) Lookup(token string) (Key, bool) {
	if s == nil || token == "" {
		return Key{}, false
	}
	snap := s.p.Load()
	if snap == nil || len(snap.byKey) == 0 {
		return Key{}, false
	}
	// 常量时间比对：遍历全部条目，命中后仍走完全程（避免按命中位置泄露信息）。
	// 密钥数量是个位数〜几十，开销可忽略。
	var (
		hit  Key
		found bool
	)
	for k, v := range snap.byKey {
		if subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
			hit = v
			found = true
		}
	}
	return hit, found
}

// ByID 按不透明标识（Fingerprint）定位一把密钥。
//
// 存在的理由：面板列表默认**脱敏**回显（见 Redact），前端手里往往只有
// "wb-a******(1)" 这种形态；拿它去比对明文必然不命中，于是删除/轮换/改域
// 全部 404（运维视角就是「删不掉」）。id 是 key 的 SHA-256 前 16 位，不可逆、
// 不泄漏明文，但足以在写操作里精确定位到唯一一条。
func (s *Store) ByID(id string) (Key, bool) {
	if s == nil || id == "" {
		return Key{}, false
	}
	want := strings.ToLower(strings.TrimSpace(id))
	if want == "" {
		return Key{}, false
	}
	snap := s.p.Load()
	if snap == nil {
		return Key{}, false
	}
	for _, k := range snap.list {
		if Fingerprint(k.Key) == want {
			return k, true
		}
	}
	return Key{}, false
}

// Replace 整体替换密钥列表（面板保存时调用），返回校验错误列表。
// 非法条目被跳过（与 New 同语义），其余条目照常生效。
func (s *Store) Replace(keys []Key) []error {
	normalized, errs := Normalize(keys)
	s.mu.Lock()
	s.p.Store(buildSnapshot(normalized))
	s.mu.Unlock()
	return errs
}

// Add 新增一把密钥并返回落定后的条目（含自动生成的 CreateAt）。
// 密钥值重复返回 ErrDuplicate；realm 非法 / pattern 语法错误返回对应错误。
func (s *Store) Add(k Key) (Key, error) {
	realm, err := NormalizeRealm(k.Realm)
	if err != nil {
		return Key{}, err
	}
	if err := ValidateKey(k.Key); err != nil {
		return Key{}, err
	}
	// pattern 语法在写入前就校验：非法规则存进去会永远不匹配（静默失效）。
	if patErrs := append(ValidatePatterns(k.AllowModels), ValidatePatterns(k.DenyModels)...); len(patErrs) > 0 {
		return Key{}, errors.Join(patErrs...)
	}
	if _, found := s.Lookup(k.Key); found {
		return Key{}, ErrDuplicate
	}
	k.Realm = realm
	k.Name = strings.TrimSpace(k.Name)
	if k.Name == "" {
		k.Name = RealmLabel(realm) + "密钥"
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now()
	}
	k.AllowModels = normalizePatterns(k.AllowModels)
	k.DenyModels = normalizePatterns(k.DenyModels)
	k.filter = compileFilter(k.AllowModels, k.DenyModels)

	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.p.Load()
	next := make([]Key, 0, len(snap.list)+1)
	next = append(next, snap.list...)
	next = append(next, k)
	s.p.Store(buildSnapshot(next))
	return k, nil
}

// KeyUpdate 一次更新的可变字段集合（nil = 该项不改）。
//
// 用结构体而不是继续加位置参数：Update 已从 5 个参数涨到 7 个，位置参数
// 在调用点会变成一串无法核对的 nil（谁是谁全靠数），且下一个字段又要改签名。
type KeyUpdate struct {
	Name        *string
	Realm       *string
	Disabled    *bool
	Note        *string
	AllowModels *[]string
	DenyModels  *[]string
}

// Update 按密钥值定位并改写条目的可变字段。
// key 本身不可改（它是对外契约）——要换密钥值请用 Rotate。
func (s *Store) Update(token string, up KeyUpdate) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.p.Load()
	if snap == nil {
		return Key{}, ErrNotFound
	}
	cur, ok := snap.byKey[token]
	if !ok {
		return Key{}, ErrNotFound
	}
	if up.Name != nil {
		cur.Name = strings.TrimSpace(*up.Name)
	}
	if up.Realm != nil {
		normalized, err := NormalizeRealm(*up.Realm)
		if err != nil {
			return Key{}, err
		}
		cur.Realm = normalized
	}
	if up.Disabled != nil {
		cur.Disabled = *up.Disabled
	}
	if up.Note != nil {
		cur.Note = *up.Note
	}
	// 模型过滤：nil = 不改；指向空切片 = 显式清空（与「不改」区分开，
	// 否则面板无法把「有规则」改回「不限制」）。
	// 先校验语法再落定：Update 失败时必须保持条目原样（不能改一半）。
	if up.AllowModels != nil {
		if patErrs := ValidatePatterns(*up.AllowModels); len(patErrs) > 0 {
			return Key{}, errors.Join(patErrs...)
		}
	}
	if up.DenyModels != nil {
		if patErrs := ValidatePatterns(*up.DenyModels); len(patErrs) > 0 {
			return Key{}, errors.Join(patErrs...)
		}
	}
	if up.AllowModels != nil {
		cur.AllowModels = normalizePatterns(*up.AllowModels)
	}
	if up.DenyModels != nil {
		cur.DenyModels = normalizePatterns(*up.DenyModels)
	}
	cur.filter = compileFilter(cur.AllowModels, cur.DenyModels)
	if cur.Name == "" {
		cur.Name = RealmLabel(cur.Realm) + "密钥"
	}

	next := make([]Key, 0, len(snap.list))
	for _, k := range snap.list {
		if k.Key == token {
			next = append(next, cur)
			continue
		}
		next = append(next, k)
	}
	s.p.Store(buildSnapshot(next))
	return cur, nil
}

// Remove 删除一把密钥；不存在返回 ErrNotFound。
func (s *Store) Remove(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.p.Load()
	if snap == nil {
		return ErrNotFound
	}
	if _, ok := snap.byKey[token]; !ok {
		return ErrNotFound
	}
	next := make([]Key, 0, len(snap.list)-1)
	for _, k := range snap.list {
		if k.Key == token {
			continue
		}
		next = append(next, k)
	}
	s.p.Store(buildSnapshot(next))
	return nil
}

// Rotate 轮换：为指定密钥生成新值，保留 name/realm/disabled/note。
// 返回更新后的条目（含新明文）。旧值立即失效。
func (s *Store) Rotate(token string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.p.Load()
	if snap == nil {
		return Key{}, ErrNotFound
	}
	cur, ok := snap.byKey[token]
	if !ok {
		return Key{}, ErrNotFound
	}
	newKey, err := Generate()
	if err != nil {
		return Key{}, err
	}
	// 新值与既有条目碰撞的概率可忽略，但仍显式检查（生成器退化时不至于静默覆盖）。
	for _, k := range snap.list {
		if k.Key == newKey {
			return Key{}, ErrDuplicate
		}
	}
	cur.Key = newKey
	cur.CreatedAt = time.Now() // 轮换视为「新密钥」：置顶并刷新审计时间

	next := make([]Key, 0, len(snap.list))
	for _, k := range snap.list {
		if k.Key == token {
			k = cur
		}
		next = append(next, k)
	}
	s.p.Store(buildSnapshot(next))
	return cur, nil
}

// RealmFor 返回某把已鉴权密钥的域绑定（""=不限）。供 handler 在请求上下文中
// 传递域约束——单独成函数是为了让调用点读起来就是「这把密钥绑哪个域」。
func (k Key) RealmFor() string { return k.Realm }

// Fingerprint 返回密钥的不透明标识：SHA-256 的前 16 个 hex 字符（64 bit）。
//
// 用途是**定位**而不是鉴定：面板列表脱敏后（Redact）前端只剩 "wb-a******(1)"，
// 用它提交写操作（删除/轮换/停用/改域）永远不会命中；而把明文下发到列表又等于
// 把凭证摊在共享屏/截图里。id 是这两者之间的折中：由明文单向派生、不可逆、
// 不随展示形态变化（同一个 key 的 id 永远相同），只在本进程的内存列表里出现。
//
// 为什么截 64 bit 而不是全长：列表里通常几把到几十把密钥，64 bit 的碰撞概率
// 可以忽略；短 id 也让 data-* 属性与日志读起来不刺眼。定位用的值不承担任何
// 密码学信任（能改配置的人本来就能读明文）。
func Fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// ID 该条目的不透明标识（见 Fingerprint）。面板用它替代明文定位条目。
func (k Key) ID() string { return Fingerprint(k.Key) }

// Redact 返回脱敏后的密钥（保留前缀与末 4 位，供面板列表展示）。
//
// 面板默认只回显脱敏值：密钥是长期凭证，列表页在共享屏幕/截图场景太容易泄漏。
// 需要明文时由用户显式「复制/显示」单条触发（见 panel 接口的 reveal 参数）。
func Redact(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", 6) + key[len(key)-4:]
}
