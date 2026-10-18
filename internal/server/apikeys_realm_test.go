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

// TestAPIKeyRealmRouting 密钥绑域后的端到端路由断言（本次功能的核心保证）：
//
//	国内版密钥 + 裸模型名 → 只打 CN 上游
//	国际版密钥 + 裸模型名 → 只打 global 上游（不因裸名默认回落 cn）
//	主密钥     + 裸模型名 → 仍走 cn（历史行为，老部署零迁移）
//	主密钥     + global: 前缀 → 走 global（主密钥不限域）
//
// 断言打在「哪个上游 base 被访问」上而不是响应码：打错域也能 200，只看状态码
// 看不出跨域泄漏。两个域的上游 base 指向不同的假服务器来区分。
func TestAPIKeyRealmRouting(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	newHandler := func(t *testing.T) (h *Handler, cnHits *hitCounter, glHits *hitCounter, primary, cnKey, glKey string) {
		t.Helper()
		cnHits, glHits = &hitCounter{}, &hitCounter{}
		cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cnHits.inc(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sseOK)
		}))
		t.Cleanup(cnSrv.Close)
		glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			glHits.inc(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sseOK)
		}))
		t.Cleanup(glSrv.Close)

		p := pool.New("")
		p.SetRandomSource(func(n int64) int64 { return 0 })
		p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999,
			Domain: "www.codebuddy.cn"})
		p.SetCredits("cn-1", 1000, 0)
		gl := &auth.Auth{UID: "gl-1", AccessToken: "at-gl", ExpiresAt: 9999999999,
			Domain: "www.workbuddy.ai"}
		if _, err := auth.BackfillRealmFor(gl, "global"); err != nil {
			t.Fatalf("BackfillRealmFor: %v", err)
		}
		p.Add(gl)
		p.SetCredits("gl-1", 1000, 0)

		up := &upstream.Client{
			HTTP:              cnSrv.Client(),
			GlobalEnabled:     true,
			ChatBaseCN:        cnSrv.URL,
			BillingBaseCN:     cnSrv.URL,
			ChatBaseGlobal:    glSrv.URL,
			BillingBaseGlobal: glSrv.URL,
		}

		cnKey, glKey = "wb-cnkey00000000001", "wb-glkey00000000001"
		store, errs := apikeys.New([]apikeys.Key{
			{Name: "国内版", Key: cnKey, Realm: apikeys.RealmCN},
			{Name: "国际版", Key: glKey, Realm: apikeys.RealmGlobal},
		})
		if len(errs) != 0 {
			t.Fatalf("apikeys.New errs=%v", errs)
		}
		primary = "primary-key"
		return NewHandler(Config{Pool: p, Upstream: up, APIKey: primary, APIKeys: store}),
			cnHits, glHits, primary, cnKey, glKey
	}

	chat := func(t *testing.T, h *Handler, model, bearer string) *httptest.ResponseRecorder {
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
		return rec
	}

	t.Run("国内版密钥裸名走CN", func(t *testing.T) {
		h, cnHits, glHits, _, cnKey, _ := newHandler(t)
		rec := chat(t, h, "glm-5.2", cnKey)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := cnHits.get(); len(got) != 1 || got[0] != "Bearer at-cn" {
			t.Errorf("CN 上游收到 %v want [Bearer at-cn]", got)
		}
		if got := glHits.get(); len(got) != 0 {
			t.Errorf("国内版密钥泄漏到 global 上游: %v", got)
		}
	})

	t.Run("国际版密钥裸名走global", func(t *testing.T) {
		h, cnHits, glHits, _, _, glKey := newHandler(t)
		rec := chat(t, h, "glm-5.2", glKey)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := glHits.get(); len(got) != 1 || got[0] != "Bearer at-gl" {
			t.Errorf("global 上游收到 %v want [Bearer at-gl]（裸名未按密钥域路由）", got)
		}
		if got := cnHits.get(); len(got) != 0 {
			t.Errorf("国际版密钥泄漏到 CN 上游: %v", got)
		}
	})

	t.Run("主密钥裸名仍走CN", func(t *testing.T) {
		h, cnHits, glHits, primary, _, _ := newHandler(t)
		rec := chat(t, h, "glm-5.2", primary)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := cnHits.get(); len(got) != 1 {
			t.Errorf("主密钥裸名应走 CN，命中=%d", len(got))
		}
		if got := glHits.get(); len(got) != 0 {
			t.Errorf("主密钥裸名不该碰 global: %v", got)
		}
	})

	t.Run("主密钥带前缀可跨域", func(t *testing.T) {
		h, cnHits, glHits, primary, _, _ := newHandler(t)
		rec := chat(t, h, "global:glm-5.2", primary)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := glHits.get(); len(got) != 1 {
			t.Errorf("主密钥 global: 前缀应走 global，命中=%d", len(got))
		}
		if got := cnHits.get(); len(got) != 0 {
			t.Errorf("主密钥 global: 前缀不该碰 CN: %v", got)
		}
	})
}

// TestAPIKeyRealmMismatchReturns400 显式写相反域前缀 → 400 且不打上游。
//
// 静默改域会让客户端以为在打国际版、实际烧国内版积分——计费错位比直接报错
// 难排查得多，所以选择硬失败。
func TestAPIKeyRealmMismatchReturns400(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseOK)
	}))
	defer srv.Close()

	p := pool.New("")
	p.SetRandomSource(func(n int64) int64 { return 0 })
	p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	p.SetCredits("cn-1", 1000, 0)
	gl := &auth.Auth{UID: "gl-1", AccessToken: "at-gl", ExpiresAt: 9999999999}
	if _, err := auth.BackfillRealmFor(gl, "global"); err != nil {
		t.Fatal(err)
	}
	p.Add(gl)
	p.SetCredits("gl-1", 1000, 0)

	up := &upstream.Client{
		HTTP: srv.Client(), GlobalEnabled: true,
		ChatBaseCN: srv.URL, BillingBaseCN: srv.URL,
		ChatBaseGlobal: srv.URL, BillingBaseGlobal: srv.URL,
	}
	cnKey, glKey := "wb-cnkey00000000001", "wb-glkey00000000001"
	store, _ := apikeys.New([]apikeys.Key{
		{Name: "国内版", Key: cnKey, Realm: apikeys.RealmCN},
		{Name: "国际版", Key: glKey, Realm: apikeys.RealmGlobal},
	})
	h := NewHandler(Config{Pool: p, Upstream: up, APIKey: "primary-key", APIKeys: store})

	for _, c := range []struct{ name, bearer, model string }{
		{"国内版密钥+global前缀", cnKey, "global:glm-5.2"},
		{"国际版密钥+cn前缀", glKey, "cn:glm-5.2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"model": c.model, "messages": []map[string]string{{"role": "user", "content": "hi"}},
			})
			r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+c.bearer)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want 400 body=%s", rec.Code, rec.Body.String())
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte("realm_mismatch")) {
				t.Errorf("错误码应为 realm_mismatch: %s", rec.Body.String())
			}
		})
	}
	if hits != 0 {
		t.Errorf("域冲突请求打了 %d 次上游，应为 0", hits)
	}
}

// TestAPIKeyMatchingPrefixAllowed 前缀与密钥域一致时不误伤。
func TestAPIKeyMatchingPrefixAllowed(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var cnHits, glHits int
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnHits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseOK)
	}))
	defer cnSrv.Close()
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		glHits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseOK)
	}))
	defer glSrv.Close()

	p := pool.New("")
	p.SetRandomSource(func(n int64) int64 { return 0 })
	p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"})
	p.SetCredits("cn-1", 1000, 0)
	gl := &auth.Auth{UID: "gl-1", AccessToken: "at-gl", ExpiresAt: 9999999999}
	if _, err := auth.BackfillRealmFor(gl, "global"); err != nil {
		t.Fatal(err)
	}
	p.Add(gl)
	p.SetCredits("gl-1", 1000, 0)

	up := &upstream.Client{
		HTTP: cnSrv.Client(), GlobalEnabled: true,
		ChatBaseCN: cnSrv.URL, BillingBaseCN: cnSrv.URL,
		ChatBaseGlobal: glSrv.URL, BillingBaseGlobal: glSrv.URL,
	}
	cnKey, glKey := "wb-cnkey00000000001", "wb-glkey00000000001"
	store, _ := apikeys.New([]apikeys.Key{
		{Name: "国内版", Key: cnKey, Realm: apikeys.RealmCN},
		{Name: "国际版", Key: glKey, Realm: apikeys.RealmGlobal},
	})
	h := NewHandler(Config{Pool: p, Upstream: up, APIKey: "primary-key", APIKeys: store})

	send := func(model, bearer string) int {
		body, _ := json.Marshal(map[string]any{
			"model": model, "stream": true,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	if code := send("cn:glm-5.2", cnKey); code != http.StatusOK {
		t.Fatalf("国内版密钥+cn前缀 code=%d want 200", code)
	}
	if code := send("global:glm-5.2", glKey); code != http.StatusOK {
		t.Fatalf("国际版密钥+global前缀 code=%d want 200", code)
	}
	if cnHits == 0 || glHits == 0 {
		t.Errorf("两域各应被访问一次: cn=%d global=%d", cnHits, glHits)
	}
}

// TestDisabledKeyRejected 停用密钥 → 401 且错误码可区分。
func TestDisabledKeyRejected(t *testing.T) {
	cnKey := "wb-cnkey00000000001"
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999})
	store, _ := apikeys.New([]apikeys.Key{{Name: "国内版", Key: cnKey, Realm: apikeys.RealmCN}})
	h := NewHandler(Config{Pool: p, Upstream: newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	}), APIKey: "primary-key", APIKeys: store})

	disabled := true
	if _, err := store.Update(cnKey, apikeys.KeyUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"model": "glm-5.2", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+cnKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("停用密钥 code=%d want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "api_key_disabled") {
		t.Errorf("错误码应区分「已停用」: %s", rec.Body.String())
	}
}

// TestUnknownKeyRejected 未知密钥 → 401 invalid_api_key。
func TestUnknownKeyRejected(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999})
	store, _ := apikeys.New(nil)
	h := NewHandler(Config{Pool: p, Upstream: newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	}), APIKey: "primary-key", APIKeys: store})

	body, _ := json.Marshal(map[string]any{
		"model": "glm-5.2", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer wb-nonexistent00001")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未知密钥 code=%d want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_api_key") {
		t.Errorf("错误码: %s", rec.Body.String())
	}
}

// hitCounter 记录某域上游收到的 Authorization 头。
type hitCounter struct {
	mu   sync.Mutex
	auth []string
}

func (h *hitCounter) inc(authz string) {
	h.mu.Lock()
	h.auth = append(h.auth, authz)
	h.mu.Unlock()
}

func (h *hitCounter) get() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.auth...)
}
