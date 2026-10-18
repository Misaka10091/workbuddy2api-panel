package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/apikeys"
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// filterHandler 构造一把带模型过滤的密钥，返回 handler 与该密钥值。
// hits 记录打到上游的次数——被过滤拒绝的请求不该透出去（否则等于白拦）。
func filterHandler(t *testing.T, allow, deny []string) (h *Handler, key string, hits *int32, modelIDs []string) {
	t.Helper()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var n int32
	var mu sync.Mutex
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			n++
			mu.Unlock()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}

	p := pool.New("")
	p.SetRandomSource(func(int64) int64 { return 0 })
	p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999,
		Domain: "www.codebuddy.cn"})
	p.SetCredits("cn-1", 1000, 0)

	key = "wb-filterkey0000001"
	store, errs := apikeys.New([]apikeys.Key{{
		Name: "带过滤", Key: key, Realm: apikeys.RealmCN,
		AllowModels: allow, DenyModels: deny,
	}})
	if len(errs) != 0 {
		t.Fatalf("apikeys.New errs=%v", errs)
	}
	h = NewHandler(Config{Pool: p, Upstream: up, APIKey: "primary-key", APIKeys: store})
	return h, key, &n, modelIDs
}

// sendChat 发一个 chat 请求，返回状态码与 body。
func sendChat(t *testing.T, h *Handler, model, bearer string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// TestKeyModelFilterBlocksChat 命中黑名单 / 不在白名单 → 403 且不打上游。
//
// 关键断言是「不透出去」：如果网关只是返回 403 但已经把请求打到上游，
// 那过滤就形同虚设（积分照烧、账号照被记录）。
func TestKeyModelFilterBlocksChat(t *testing.T) {
	h, key, hits, _ := filterHandler(t, []string{"glm-*"}, []string{"glm-5.1"})

	// 允许
	if code, body := sendChat(t, h, "glm-5.2", key); code != http.StatusOK {
		t.Fatalf("glm-5.2 code=%d body=%s want 200", code, body)
	}
	before := *hits

	// 黑名单命中 → 403
	code, body := sendChat(t, h, "glm-5.1", key)
	if code != http.StatusForbidden {
		t.Fatalf("glm-5.1(黑名单) code=%d want 403 body=%s", code, body)
	}
	if !strings.Contains(body, "model_not_allowed") {
		t.Errorf("错误码应为 model_not_allowed: %s", body)
	}

	// 不在白名单 → 403
	code, body = sendChat(t, h, "hy3", key)
	if code != http.StatusForbidden {
		t.Fatalf("hy3(不在白名单) code=%d want 403 body=%s", code, body)
	}
	if *hits != before {
		t.Errorf("被过滤拒绝的请求仍打到了上游：%d → %d", before, *hits)
	}
}

// TestKeyModelFilterWildcards 通配符在真实请求路径上生效。
func TestKeyModelFilterWildcards(t *testing.T) {
	cases := []struct {
		name  string
		allow []string
		deny  []string
		model string
		want  int
	}{
		{"星号后缀放行", []string{"glm-*"}, nil, "glm-5.2", 200},
		{"星号后缀拒绝", []string{"glm-*"}, nil, "hy3", 403},
		{"问号单字符", []string{"glm-?.?"}, nil, "glm-5.3", 200},
		{"问号单字符不匹配", []string{"glm-?.?"}, nil, "glm-5.3-flash", 403},
		{"黑名单优先", []string{"glm-*"}, []string{"*-flash"}, "glm-5.3-flash", 403},
		{"黑名单优先但同类放行", []string{"glm-*"}, []string{"*-flash"}, "glm-5.2", 200},
		{"纯黑名单放行其他", nil, []string{"gpt-*"}, "glm-5.2", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, key, _, _ := filterHandler(t, c.allow, c.deny)
			if code, body := sendChat(t, h, c.model, key); code != c.want {
				t.Errorf("model=%s code=%d want %d body=%s", c.model, code, c.want, body)
			}
		})
	}
}

// TestKeyModelFilterIgnoresRealmPrefix 过滤判定用裸模型名：
// 密钥绑了域时客户端可能仍带 cn: 前缀（迁移期），不该因此绕过或误拒。
func TestKeyModelFilterIgnoresRealmPrefix(t *testing.T) {
	h, key, _, _ := filterHandler(t, []string{"glm-*"}, []string{"glm-5.1"})

	if code, body := sendChat(t, h, "cn:glm-5.2", key); code != http.StatusOK {
		t.Errorf("cn:glm-5.2 code=%d want 200 body=%s", code, body)
	}
	if code, _ := sendChat(t, h, "cn:glm-5.1", key); code != http.StatusForbidden {
		t.Errorf("cn:glm-5.1 code=%d want 403（前缀不该绕过黑名单）", code)
	}
}

// TestPrimaryKeyIgnoresModelFilter 主密钥没有过滤规则，任何模型都放行到上游。
func TestPrimaryKeyIgnoresModelFilter(t *testing.T) {
	h, _, hits, _ := filterHandler(t, []string{"glm-*"}, nil)
	before := *hits
	if code, body := sendChat(t, h, "hy3", "primary-key"); code != http.StatusOK {
		t.Fatalf("主密钥 hy3 code=%d want 200 body=%s", code, body)
	}
	if *hits == before {
		t.Error("主密钥请求未打到上游")
	}
}

// TestFilterModelsByKeyUnit 列表过滤的纯函数行为（含 nil/空规则快路径）。
func TestFilterModelsByKeyUnit(t *testing.T) {
	list := []map[string]any{
		{"id": "glm-5.2"}, {"id": "glm-5.1"}, {"id": "hy3"}, {"id": "gpt-6-sol"},
	}

	// nil 规则 → 原样返回**同一个**切片（零开销快路径）
	got := filterModelsByKey(list, nil)
	if len(got) != len(list) {
		t.Fatalf("nil filter 过滤了 %d/%d 条", len(got), len(list))
	}

	f := apikeys.Key{AllowModels: []string{"glm-*"}, DenyModels: []string{"glm-5.1"}}
	store, _ := apikeys.New([]apikeys.Key{{
		Name: "x", Key: "wb-filtertest00001", Realm: "cn",
		AllowModels: f.AllowModels, DenyModels: f.DenyModels,
	}})
	k, _ := store.Lookup("wb-filtertest00001")
	filtered := filterModelsByKey(list, k.Filter())
	if len(filtered) != 1 || filtered[0]["id"] != "glm-5.2" {
		t.Errorf("过滤结果=%v want 仅 glm-5.2", filtered)
	}
}

// TestFilterMatchesModelIDNotName 过滤依据**模型 ID**，不是展示名称。
//
// 为什么必须钉死这条边界：目录里 id 与 name 完全不同（id="glm-5.3"、
// name="[国内版] GLM-5.3"）。运维在界面上看到的是名称，若按名称匹配，
// 写成 "GLM-5.3" 甚至 "[国内版] GLM-5.3" 都不会命中——规则静默失效，
// 看起来"限制生效了"实际全放行。而 name 是展示文案、随时可能因上游改字
// 而变，只有 id 是调用契约。
func TestFilterMatchesModelIDNotName(t *testing.T) {
	// 目录条目：id 与 name 故意不同（与真实上游一致）
	list := []map[string]any{
		{"id": "glm-5.3", "name": "[国内版] GLM-5.3"},
		{"id": "deepseek-v4.1-flash", "name": "[国内版] Deepseek-V4.1-Flash"},
	}

	// 按 ID 规则：命中
	byID, err := apikeys.New([]apikeys.Key{{
		Name: "by-id", Key: "wb-byid00000000001", Realm: "cn",
		AllowModels: []string{"glm-5.3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := byID.Lookup("wb-byid00000000001")
	if got := filterModelsByKey(list, kid.Filter()); len(got) != 1 || got[0]["id"] != "glm-5.3" {
		t.Errorf("按 ID 规则应只留 glm-5.3，实得 %v", got)
	}

	// 按 name 规则：不命中（这正是要防止的误解）
	byName, err := apikeys.New([]apikeys.Key{{
		Name: "by-name", Key: "wb-byname000000001", Realm: "cn",
		AllowModels: []string{"[国内版] GLM-5.3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	kname, _ := byName.Lookup("wb-byname000000001")
	if got := filterModelsByKey(list, kname.Filter()); len(got) != 0 {
		t.Errorf("按 name 写规则不该命中任何模型（证明过滤看的是 id），实得 %v", got)
	}
}
