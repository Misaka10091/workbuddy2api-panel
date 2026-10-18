package apikeys

import (
	"errors"
	"strings"
	"testing"
)

// TestNormalizeRealmRejectsUnknown 域枚举必须精确：密钥域是计费隔离边界，
// 拼错要显式失败，不能静默回落成"不限域"（那等于悄悄放宽了约束）。
func TestNormalizeRealmRejectsUnknown(t *testing.T) {
	ok := map[string]string{
		"":         RealmAny,
		"cn":       RealmCN,
		"global":   RealmGlobal,
		"CN":       RealmCN,
		"Global":   RealmGlobal,
		"  cn  ":   RealmCN,
	}
	for in, want := range ok {
		got, err := NormalizeRealm(in)
		if err != nil {
			t.Errorf("NormalizeRealm(%q) err=%v want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("NormalizeRealm(%q)=%q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"jp", "cnn", "globalx", "all", "0"} {
		if got, err := NormalizeRealm(bad); err == nil {
			t.Errorf("NormalizeRealm(%q)=(%q,nil) want error", bad, got)
		}
	}
}

// TestValidateKey 密钥值校验：空白/过短/过长都拒。
func TestValidateKey(t *testing.T) {
	good := []string{"wb-0123456789abcdef", "12345678", strings.Repeat("a", 256)}
	for _, k := range good {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) err=%v want nil", truncate(k), err)
		}
	}
	bad := []string{"", "  ", "abc", "with space", "tab\tkey", strings.Repeat("a", 257)}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) err=nil want error", truncate(k))
		}
	}
}

func truncate(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// TestStoreAddLookupRemove 基本生命周期：新增 → 可按明文查到绑定的域 → 删除后查不到。
func TestStoreAddLookupRemove(t *testing.T) {
	s, errs := New(nil)
	if len(errs) != 0 {
		t.Fatalf("New(nil) errs=%v", errs)
	}
	if s.Len() != 0 {
		t.Fatalf("空表 Len=%d want 0", s.Len())
	}

	added, err := s.Add(Key{Name: "国际版密钥", Key: "wb-aaaaaaaaaaaaaaaa", Realm: "global"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.Realm != RealmGlobal {
		t.Errorf("added realm=%q want global", added.Realm)
	}
	if added.CreatedAt.IsZero() {
		t.Error("CreatedAt 未自动填充")
	}

	got, found := s.Lookup("wb-aaaaaaaaaaaaaaaa")
	if !found {
		t.Fatal("Lookup 未命中刚添加的密钥")
	}
	if got.Realm != RealmGlobal || got.Name != "国际版密钥" {
		t.Errorf("Lookup=%+v 与添加时不一致", got)
	}

	if err := s.Remove("wb-aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, found := s.Lookup("wb-aaaaaaaaaaaaaaaa"); found {
		t.Error("删除后仍能查到")
	}
	if err := s.Remove("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove(不存在)=%v want ErrNotFound", err)
	}
}

// TestStoreRejectsDuplicate 同一密钥值不能绑两个域（否则判定歧义）。
func TestStoreRejectsDuplicate(t *testing.T) {
	s, _ := New([]Key{{Name: "a", Key: "wb-duplicate000001", Realm: "cn"}})
	_, err := s.Add(Key{Name: "b", Key: "wb-duplicate000001", Realm: "global"})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("Add 重复=%v want ErrDuplicate", err)
	}
}

// TestStoreDisabledStillFound 停用的密钥仍能查到（但 IsDisabled），
// 这样 401 文案能区分「无效」与「已停用」。
func TestStoreDisabledStillFound(t *testing.T) {
	s, _ := New([]Key{{Name: "停用号", Key: "wb-disabled0000001", Realm: "cn", Disabled: true}})
	got, found := s.Lookup("wb-disabled0000001")
	if !found {
		t.Fatal("停用密钥应仍可查到（用于区分 401 原因）")
	}
	if !got.Disabled {
		t.Error("Disabled 标记丢失")
	}
}

// TestStoreUpdateRealm 改域是核心运维动作：cn → global 后 Lookup 必须反映新值。
func TestStoreUpdateRealm(t *testing.T) {
	s, _ := New([]Key{{Name: "x", Key: "wb-realmchange00001", Realm: "cn"}})
	realm := "global"
	updated, err := s.Update("wb-realmchange00001", KeyUpdate{Realm: &realm})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Realm != RealmGlobal {
		t.Errorf("updated realm=%q want global", updated.Realm)
	}
	got, _ := s.Lookup("wb-realmchange00001")
	if got.Realm != RealmGlobal {
		t.Errorf("Lookup 后 realm=%q want global（快照未重建）", got.Realm)
	}
	// 非法域必须报错且不改动现值。
	bad := "jp"
	if _, err := s.Update("wb-realmchange00001", KeyUpdate{Realm: &bad}); err == nil {
		t.Error("Update 非法域 err=nil want error")
	}
	if got, _ := s.Lookup("wb-realmchange00001"); got.Realm != RealmGlobal {
		t.Errorf("非法域更新后 realm 被改成了 %q", got.Realm)
	}
}

// TestStoreRotateKeepsBinding 轮换换值但保留名称与域绑定，旧值立即失效。
func TestStoreRotateKeepsBinding(t *testing.T) {
	s, _ := New([]Key{{Name: "要轮换的", Key: "wb-oldkey000000001", Realm: "global", Note: "备注"}})
	rotated, err := s.Rotate("wb-oldkey000000001")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.Key == "wb-oldkey000000001" {
		t.Error("轮换未换值")
	}
	if rotated.Realm != RealmGlobal || rotated.Name != "要轮换的" || rotated.Note != "备注" {
		t.Errorf("轮换丢了元数据: %+v", rotated)
	}
	if _, found := s.Lookup("wb-oldkey000000001"); found {
		t.Error("旧值仍有效（轮换未失效旧密钥）")
	}
	if _, found := s.Lookup(rotated.Key); !found {
		t.Error("新值查不到")
	}
}

// TestNewSkipsInvalidEntries 脏条目跳过而不让整表失败（一条脏密钥不该拖垮网关启动）。
func TestNewSkipsInvalidEntries(t *testing.T) {
	s, errs := New([]Key{
		{Name: "好", Key: "wb-good0000000001", Realm: "cn"},
		{Name: "坏域", Key: "wb-badrealm000001", Realm: "jp"},
		{Name: "空值", Key: "", Realm: "cn"},
		{Name: "重复", Key: "wb-good0000000001", Realm: "global"},
		{Name: "好2", Key: "wb-good0000000002", Realm: "global"},
	})
	if len(errs) != 3 {
		t.Errorf("errs=%d want 3（坏域/空值/重复）: %v", len(errs), errs)
	}
	if s.Len() != 2 {
		t.Errorf("Len=%d want 2（两条合法条目）", s.Len())
	}
	// 重复条目被丢的是后一条：先出现的域胜出（确定性）。
	if k, _ := s.Lookup("wb-good0000000001"); k.Realm != RealmCN {
		t.Errorf("重复条目胜出者 realm=%q want cn（先到先得）", k.Realm)
	}
}

// TestGenerateProducesUsableKey 生成的密钥必须能通过自身校验（别生成出无法使用的值）。
func TestGenerateProducesUsableKey(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		k, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if err := ValidateKey(k); err != nil {
			t.Fatalf("Generate 产出不可用密钥 %q: %v", k, err)
		}
		if seen[k] {
			t.Fatalf("Generate 重复产出 %q", k)
		}
		seen[k] = true
	}
}

// TestRedact 脱敏保留首尾，长度不足全星号（面板列表默认展示形态）。
func TestRedact(t *testing.T) {
	if got := Redact("wb-0123456789abcdef"); got != "wb-0******cdef" {
		t.Errorf("Redact=%q", got)
	}
	if got := Redact("short"); got != "*****" {
		t.Errorf("Redact(short)=%q want 全星号", got)
	}
	// 脱敏值不能反过来泄露完整密钥。
	full := "wb-supersecretvalue42"
	if strings.Contains(Redact(full), "secret") {
		t.Errorf("Redact 泄漏了中段: %q", Redact(full))
	}
}

// TestStoreNilSafe nil Store 的所有读方法都必须安全（handler 在未启用多密钥时持有 nil）。
func TestStoreNilSafe(t *testing.T) {
	var s *Store
	if s.Len() != 0 {
		t.Error("nil Len != 0")
	}
	if s.List() != nil {
		t.Error("nil List != nil")
	}
	if _, found := s.Lookup("anything"); found {
		t.Error("nil Lookup 命中了")
	}
}

// TestRealmLabel 标签口径（面板与日志共用，避免两处各写一份中文）。
func TestRealmLabel(t *testing.T) {
	cases := map[string]string{
		RealmCN: "国内版", RealmGlobal: "国际版", RealmAny: "不限域", "weird": "不限域",
	}
	for in, want := range cases {
		if got := RealmLabel(in); got != want {
			t.Errorf("RealmLabel(%q)=%q want %q", in, got, want)
		}
	}
}
