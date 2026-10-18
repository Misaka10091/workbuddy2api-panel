package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/apikeys"
)

// newKeyPanel 构造带密钥表的面板；saved 收集 SaveKeys 落盘的密钥表。
func newKeyPanel(t *testing.T, initial []apikeys.Key) (*Panel, *apikeys.Store, *[]apikeys.Key) {
	t.Helper()
	store, errs := apikeys.New(initial)
	if len(errs) != 0 {
		t.Fatalf("apikeys.New errs=%v", errs)
	}
	saved := new([]apikeys.Key)
	p := New(Config{
		Version:  "test",
		APIKey:   "panel-key",
		APIKeys:  store,
		SaveKeys: func(keys []apikeys.Key) error { *saved = keys; return nil },
	})
	return p, store, saved
}

// keyReq 构造带面板鉴权的请求。
func keyReq(method, path string, body any) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
	}
	r.Header.Set("Authorization", "Bearer panel-key")
	return r
}

// TestAPIKeysListRedacted by default: 列表默认脱敏，主密钥单列且标 primary。
//
// 默认脱敏的原因：密钥是长期凭证，列表页在共享屏幕/截图场景太容易泄漏。
func TestAPIKeysListRedacted(t *testing.T) {
	cnKey := "wb-cnkey00000000001"
	p, _, _ := newKeyPanel(t, []apikeys.Key{
		{Name: "国内版", Key: cnKey, Realm: apikeys.RealmCN},
		{Name: "国际版", Key: "wb-glkey00000000001", Realm: apikeys.RealmGlobal},
	})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("GET", "/panel/api/api_keys", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Keys []struct {
			Name      string `json:"name"`
			Key       string `json:"key"`
			Realm     string `json:"realm"`
			RealmLbl  string `json:"realm_label"`
			Primary   bool   `json:"primary"`
		} `json:"keys"`
		Realms []map[string]string `json:"realms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 主密钥 + 两把自定义 = 3 行
	if len(resp.Keys) != 3 {
		t.Fatalf("keys=%d want 3（主密钥 + 2 自定义）", len(resp.Keys))
	}
	if !resp.Keys[0].Primary {
		t.Error("第一行应为主密钥（primary=true）")
	}
	for _, k := range resp.Keys {
		if strings.Contains(k.Key, cnKey) {
			t.Errorf("列表默认返回了明文密钥: %q", k.Key)
		}
	}
	// 域标签由服务端给（含「不限域」），前端不再各写一份映射。
	if len(resp.Realms) != 3 {
		t.Fatalf("realms=%d want 3（国内版/国际版/不限域）", len(resp.Realms))
	}
	labels := map[string]string{}
	for _, r := range resp.Realms {
		labels[r["value"]] = r["label"]
	}
	if labels["cn"] != "国内版" || labels["global"] != "国际版" || labels[""] != "不限域" {
		t.Errorf("realm 标签不对: %v", labels)
	}
}

// TestAPIKeysListReveal ?reveal=1 才给明文（显式请求）。
func TestAPIKeysListReveal(t *testing.T) {
	cnKey := "wb-cnkey00000000001"
	p, _, _ := newKeyPanel(t, []apikeys.Key{{Name: "国内版", Key: cnKey, Realm: apikeys.RealmCN}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("GET", "/panel/api/api_keys?reveal=1", nil))
	if !strings.Contains(rec.Body.String(), cnKey) {
		t.Errorf("reveal=1 应返回明文，body=%s", rec.Body.String())
	}
}

// TestAPIKeyCreateWithRealm 创建时选国内版/国际版，落盘被调用一次。
func TestAPIKeyCreateWithRealm(t *testing.T) {
	p, store, saved := newKeyPanel(t, nil)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys", map[string]any{
		"name": "国际版给团队B", "realm": "global",
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Key   string `json:"key"`
		Realm string `json:"realm"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Realm != "global" {
		t.Fatalf("resp=%+v want ok+global", resp)
	}
	if resp.Key == "" {
		t.Fatal("未生成密钥值")
	}
	// 服务端已生效
	got, found := store.Lookup(resp.Key)
	if !found || got.Realm != apikeys.RealmGlobal {
		t.Errorf("store 内未生效: found=%v key=%+v", found, got)
	}
	if len(*saved) != 1 {
		t.Errorf("落盘次数/内容不对: %v", *saved)
	}
}

// TestAPIKeyCreateRejectsBadRealm 非法域必须 400（不能静默存成不限域）。
func TestAPIKeyCreateRejectsBadRealm(t *testing.T) {
	p, store, _ := newKeyPanel(t, nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys", map[string]any{
		"name": "坏域", "realm": "jp",
	}))
	if rec.Code != 400 {
		t.Fatalf("code=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if store.Len() != 0 {
		t.Error("非法域请求仍写入了密钥")
	}
}

// TestAPIKeyUpdateRealmSwitch 面板改绑定版本：cn → global 立即生效并落盘。
func TestAPIKeyUpdateRealmSwitch(t *testing.T) {
	cnKey := "wb-cnkey00000000001"
	p, store, saved := newKeyPanel(t, []apikeys.Key{
		{Name: "要改域的", Key: cnKey, Realm: apikeys.RealmCN},
	})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"key": cnKey, "realm": "global",
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Lookup(cnKey)
	if got.Realm != apikeys.RealmGlobal {
		t.Errorf("改域未生效: realm=%q", got.Realm)
	}
	if len(*saved) != 1 || (*saved)[0].Realm != apikeys.RealmGlobal {
		t.Errorf("落盘内容不对: %v", *saved)
	}
}

// TestAPIKeyDisableThenRejected 面板停用 → store 内 Disabled，密钥仍可查到（可再启用）。
func TestAPIKeyDisableThenRejected(t *testing.T) {
	key := "wb-cnkey00000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{Name: "x", Key: key, Realm: apikeys.RealmCN}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"key": key, "disabled": true,
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	got, found := store.Lookup(key)
	if !found || !got.Disabled {
		t.Fatalf("停用未生效: found=%v disabled=%v", found, got.Disabled)
	}

	// 再启用
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"key": key, "disabled": false,
	}))
	if rec.Code != 200 {
		t.Fatalf("重新启用 code=%d", rec.Code)
	}
	if got, _ := store.Lookup(key); got.Disabled {
		t.Error("重新启用未生效")
	}
}

// TestAPIKeyRotate 轮换返回新明文、旧值失效。
func TestAPIKeyRotate(t *testing.T) {
	old := "wb-oldkey000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{Name: "轮换", Key: old, Realm: apikeys.RealmGlobal}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/rotate", map[string]any{"key": old}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key   string `json:"key"`
		Realm string `json:"realm"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Key == "" || resp.Key == old {
		t.Fatalf("轮换未返回新值: %q", resp.Key)
	}
	if resp.Realm != apikeys.RealmGlobal {
		t.Errorf("轮换丢了域绑定: %q", resp.Realm)
	}
	if _, found := store.Lookup(old); found {
		t.Error("旧密钥仍有效")
	}
	if _, found := store.Lookup(resp.Key); !found {
		t.Error("新密钥未生效")
	}
}

// TestAPIKeyRemove 删除后查不到。
func TestAPIKeyRemove(t *testing.T) {
	key := "wb-cnkey00000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{Name: "x", Key: key, Realm: apikeys.RealmCN}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/remove", map[string]any{"key": key}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, found := store.Lookup(key); found {
		t.Error("删除后仍能查到")
	}
	// 再删 → 404
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/remove", map[string]any{"key": key}))
	if rec.Code != 404 {
		t.Errorf("重复删除 code=%d want 404", rec.Code)
	}
}

// TestAPIKeyRemoveByID 回归：面板默认脱敏，前端只能拿到 id —— 用 id 必须能删。
//
// 历史 bug（issue：创建的 key 无法删除）：列表回显 "wb-c******(01)"，前端直接把
// 屏上那个脱敏值当 key 提交，服务端按明文比对必然不命中 → 404，运维看到的就是
// 「新建的密钥删不掉」。这里把「列表 → 取 id → 删除」这条真实路径钉住。
func TestAPIKeyRemoveByID(t *testing.T) {
	key := "wb-cnkey00000000001"
	p, store, saved := newKeyPanel(t, []apikeys.Key{{Name: "要删的", Key: key, Realm: apikeys.RealmCN}})

	// 1) 默认列表（脱敏）：拿到 id 与脱敏后的展示值
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("GET", "/panel/api/api_keys", nil))
	var listed struct {
		Keys []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
			Key  string `json:"key"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var id, shown string
	for _, k := range listed.Keys {
		if k.Name == "要删的" {
			id, shown = k.ID, k.Key
		}
	}
	if id == "" {
		t.Fatal("列表未下发 id（脱敏模式下前端将无法定位条目）")
	}
	if shown == key {
		t.Fatal("默认列表泄漏了明文")
	}
	if id == shown {
		t.Fatal("id 就是展示值（应当是不透明指纹）")
	}

	// 2) 用展示值删除 → 必须失败且不误删（定位不到就不是 200）
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/remove", map[string]any{"key": shown}))
	if rec.Code != 404 {
		t.Fatalf("用脱敏值删除 code=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	if _, found := store.Lookup(key); !found {
		t.Fatal("脱敏值定位失败却把密钥删掉了")
	}

	// 3) 用 id 删除 → 200，store 与落盘都同步
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/remove", map[string]any{"id": id}))
	if rec.Code != 200 {
		t.Fatalf("用 id 删除 code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, found := store.Lookup(key); found {
		t.Error("删除后仍能查到")
	}
	// 落盘也要跟上：SaveKeys 被调用过一次，且写出的表里不再有这把密钥。
	if *saved == nil {
		t.Error("删除未落盘（SaveKeys 未被调用）")
	}
	for _, k := range *saved {
		if k.Key == key {
			t.Error("落盘内容里仍含被删的密钥")
		}
	}
}

// TestAPIKeyUpdateByID 停用/改域/改过滤同样走 id（否则刷新页面后这些按钮全 404）。
func TestAPIKeyUpdateByID(t *testing.T) {
	key := "wb-cnkey00000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{Name: "x", Key: key, Realm: apikeys.RealmCN}})
	id := apikeys.Fingerprint(key)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"id": id, "disabled": true, "realm": "global",
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Lookup(key)
	if !got.Disabled || got.Realm != apikeys.RealmGlobal {
		t.Errorf("按 id 更新未生效: disabled=%v realm=%q", got.Disabled, got.Realm)
	}

	// 定位不到的 id → 404（而不是静默成功）
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"id": "0000000000000000", "disabled": false,
	}))
	if rec.Code != 404 {
		t.Errorf("未知 id code=%d want 404", rec.Code)
	}
}

// TestAPIKeyRotateByID 轮换返回新明文与新 id；旧 id 随旧值一起失效。
func TestAPIKeyRotateByID(t *testing.T) {
	key := "wb-oldkey00000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{Name: "r", Key: key, Realm: apikeys.RealmGlobal}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/rotate", map[string]any{
		"id": apikeys.Fingerprint(key),
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key   string `json:"key"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Realm string `json:"realm"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Key == "" || resp.Key == key {
		t.Fatalf("轮换未返回新值: %q", resp.Key)
	}
	if resp.ID != apikeys.Fingerprint(resp.Key) {
		t.Errorf("响应里的 id 不是新密钥的指纹: %q", resp.ID)
	}
	if resp.Realm != apikeys.RealmGlobal {
		t.Errorf("轮换丢了域绑定: %q", resp.Realm)
	}
	if _, found := store.Lookup(key); found {
		t.Error("旧密钥仍有效")
	}
}

// TestAPIKeyCreateReturnsID 创建响应要带 id：新建完立刻删除/改域靠它定位。
func TestAPIKeyCreateReturnsID(t *testing.T) {
	p, _, _ := newKeyPanel(t, nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys", map[string]any{
		"name": "新建", "realm": "cn",
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == "" || resp.ID != apikeys.Fingerprint(resp.Key) {
		t.Fatalf("创建响应 id=%q 与 key=%q 不匹配", resp.ID, resp.Key)
	}
}

// TestAPIKeysNotAvailableWithoutStore 未启用多密钥（nil Store）→ 501，不 panic。
func TestAPIKeysNotAvailableWithoutStore(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "panel-key"})
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/panel/api/api_keys", nil},
		{"POST", "/panel/api/api_keys", map[string]any{"realm": "cn"}},
		{"POST", "/panel/api/api_keys/remove", map[string]any{"key": "x"}},
	} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, keyReq(c.method, c.path, c.body))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s code=%d want 501", c.method, c.path, rec.Code)
		}
	}
}

// TestAPIKeysRequirePanelAuth 密钥管理必须走面板鉴权（无 key 不允许读明文/改配置）。
func TestAPIKeysRequirePanelAuth(t *testing.T) {
	p, _, _ := newKeyPanel(t, []apikeys.Key{{Name: "x", Key: "wb-cnkey00000000001", Realm: "cn"}})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/api_keys?reveal=1", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无鉴权 code=%d want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "wb-cnkey00000000001") {
		t.Error("401 响应泄漏了明文密钥")
	}
}
