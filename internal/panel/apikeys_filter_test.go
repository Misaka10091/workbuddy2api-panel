package panel

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/apikeys"
)

// TestAPIKeyCreateWithModelFilter 创建时带白/黑名单，落定后 store 内规则生效。
func TestAPIKeyCreateWithModelFilter(t *testing.T) {
	p, store, saved := newKeyPanel(t, nil)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys", map[string]any{
		"name":         "限模型",
		"realm":        "cn",
		"allow_models": []string{"glm-*", "hy3"},
		"deny_models":  []string{"*-flash"},
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key         string   `json:"key"`
		AllowModels []string `json:"allow_models"`
		DenyModels  []string `json:"deny_models"`
		ModelFilter string   `json:"model_filter"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.AllowModels) != 2 || len(resp.DenyModels) != 1 {
		t.Fatalf("响应规则不对: %+v", resp)
	}
	if !strings.Contains(resp.ModelFilter, "仅允许") || !strings.Contains(resp.ModelFilter, "排除") {
		t.Errorf("model_filter 摘要不完整: %q", resp.ModelFilter)
	}

	k, found := store.Lookup(resp.Key)
	if !found {
		t.Fatal("密钥未写入 store")
	}
	// 规则真的生效（store 内的 compiled filter）
	if !k.ModelAllowed("glm-5.2") {
		t.Error("glm-5.2 应允许")
	}
	if k.ModelAllowed("glm-5.3-flash") {
		t.Error("glm-5.3-flash 命中黑名单 *-flash 应拒绝")
	}
	if k.ModelAllowed("gpt-6-sol") {
		t.Error("gpt-6-sol 不在白名单应拒绝")
	}
	if len(*saved) != 1 {
		t.Errorf("落盘次数不对: %d", len(*saved))
	}
}

// TestAPIKeyUpdateModelFilter 就地改模型过滤；空数组 = 显式清空（恢复不限制）。
//
// 「不改」与「清空」必须能区分：前端把规则全删光时要能表达恢复不限制，
// 而只提交部分字段时不能把已有规则冲掉。
func TestAPIKeyUpdateModelFilter(t *testing.T) {
	key := "wb-cnkey00000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{
		Name: "x", Key: key, Realm: apikeys.RealmCN,
		AllowModels: []string{"glm-*"},
	}})

	// 只改白名单，黑名单保持原样（本来为空）
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"key": key, "allow_models": []string{"hy3"},
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	k, _ := store.Lookup(key)
	if !k.ModelAllowed("hy3") || k.ModelAllowed("glm-5.2") {
		t.Errorf("白名单未替换: allow=%v", k.AllowModels)
	}

	// 显式清空 → 恢复不限制
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"key": key, "allow_models": []string{}, "deny_models": []string{},
	}))
	if rec.Code != 200 {
		t.Fatalf("清空 code=%d body=%s", rec.Code, rec.Body.String())
	}
	k, _ = store.Lookup(key)
	if len(k.AllowModels) != 0 || len(k.DenyModels) != 0 {
		t.Errorf("规则未清空: %+v", k)
	}
	if !k.ModelAllowed("anything-at-all") {
		t.Error("清空后应恢复不限制")
	}
}

// TestAPIKeyUpdateKeepsFilterWhenOmitted 不提交模型字段时不该动已有规则。
func TestAPIKeyUpdateKeepsFilterWhenOmitted(t *testing.T) {
	key := "wb-cnkey00000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{
		Name: "x", Key: key, Realm: apikeys.RealmCN,
		AllowModels: []string{"glm-*"},
	}})

	// 只改停用状态，不带 allow_models
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/update", map[string]any{
		"key": key, "disabled": true,
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	k, _ := store.Lookup(key)
	if len(k.AllowModels) != 1 || k.AllowModels[0] != "glm-*" {
		t.Errorf("未提交的过滤规则被清掉了: %v", k.AllowModels)
	}
	if !k.Disabled {
		t.Error("停用未生效")
	}
}

// TestAPIKeysListExposesModelFilter 列表要给前端「可用模型」列所需字段。
func TestAPIKeysListExposesModelFilter(t *testing.T) {
	p, _, _ := newKeyPanel(t, []apikeys.Key{{
		Name: "x", Key: "wb-cnkey00000000001", Realm: apikeys.RealmCN,
		AllowModels: []string{"glm-*"}, DenyModels: []string{"*-flash"},
	}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("GET", "/panel/api/api_keys", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var resp struct {
		Keys []struct {
			Primary     bool     `json:"primary"`
			AllowModels []string `json:"allow_models"`
			DenyModels  []string `json:"deny_models"`
			ModelFilter string   `json:"model_filter"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// 第 0 行是主密钥（无规则），第 1 行是自定义
	var own *struct {
		Primary     bool     `json:"primary"`
		AllowModels []string `json:"allow_models"`
		DenyModels  []string `json:"deny_models"`
		ModelFilter string   `json:"model_filter"`
	}
	for i := range resp.Keys {
		if !resp.Keys[i].Primary {
			own = &resp.Keys[i]
			break
		}
	}
	if own == nil {
		t.Fatal("列表里没有自定义密钥行")
	}
	if len(own.AllowModels) != 1 || len(own.DenyModels) != 1 {
		t.Errorf("列表未透出规则: %+v", own)
	}
	if own.ModelFilter == "" {
		t.Error("列表未透出 model_filter 摘要")
	}
}

// TestAPIKeyRotateKeepsModelFilter 轮换只换密钥值，过滤规则要保留。
func TestAPIKeyRotateKeepsModelFilter(t *testing.T) {
	old := "wb-oldkey000000001"
	p, store, _ := newKeyPanel(t, []apikeys.Key{{
		Name: "轮换", Key: old, Realm: apikeys.RealmCN,
		AllowModels: []string{"glm-*"},
	}})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, keyReq("POST", "/panel/api/api_keys/rotate", map[string]any{"key": old}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	k, found := store.Lookup(resp.Key)
	if !found {
		t.Fatal("新密钥不在 store")
	}
	if len(k.AllowModels) != 1 || k.AllowModels[0] != "glm-*" {
		t.Errorf("轮换丢了过滤规则: %v", k.AllowModels)
	}
	if !k.ModelAllowed("glm-5.2") || k.ModelAllowed("hy3") {
		t.Error("轮换后规则未生效")
	}
}

// TestDescribeFilter 摘要文案（列表展示用）。
func TestDescribeFilter(t *testing.T) {
	cases := []struct {
		allow, deny []string
		want        string
	}{
		{nil, nil, ""},
		{[]string{"glm-*"}, nil, "仅允许 glm-*"},
		{nil, []string{"*-sol"}, "排除 *-sol"},
		{[]string{"glm-*"}, []string{"glm-5.1"}, "仅允许 glm-*；排除 glm-5.1"},
	}
	for _, c := range cases {
		if got := describeFilter(c.allow, c.deny); got != c.want {
			t.Errorf("describeFilter(%v,%v)=%q want %q", c.allow, c.deny, got, c.want)
		}
	}
}
