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
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
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
// 密钥值重复返回 ErrDuplicate；realm 非法返回对应错误。
func (s *Store) Add(k Key) (Key, error) {
	realm, err := NormalizeRealm(k.Realm)
	if err != nil {
		return Key{}, err
	}
	if err := ValidateKey(k.Key); err != nil {
		return Key{}, err
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

	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.p.Load()
	next := make([]Key, 0, len(snap.list)+1)
	next = append(next, snap.list...)
	next = append(next, k)
	s.p.Store(buildSnapshot(next))
	return k, nil
}

// Update 按密钥值定位并改写条目的可变字段（name/realm/disabled/note）。
// key 本身不可改（它是对外契约）——要换密钥值请 Add 新的再 Remove 旧的。
// realm 传入指针语义：nil = 不改。
func (s *Store) Update(token string, name, realm *string, disabled *bool, note *string) (Key, error) {
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
	if name != nil {
		cur.Name = strings.TrimSpace(*name)
	}
	if realm != nil {
		normalized, err := NormalizeRealm(*realm)
		if err != nil {
			return Key{}, err
		}
		cur.Realm = normalized
	}
	if disabled != nil {
		cur.Disabled = *disabled
	}
	if note != nil {
		cur.Note = *note
	}
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
