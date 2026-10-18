package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// batchRT 测试用 http.RoundTripper（server 包里有同名私有类型，跨包不可见）。
type batchRT func(*http.Request) (*http.Response, error)

func (f batchRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// batchPanel 构造带三个账号的面板（不发任何真实上游请求：Upstream 指向会记数
// 的假 client，用到时才计）。
func batchPanel(t *testing.T, uids ...string) (*Panel, *int32) {
	t.Helper()
	var calls int32
	up := &upstream.Client{
		HTTP: &http.Client{Transport: batchRT(func(r *http.Request) (*http.Response, error) {
			calls++
			// 全部回成"今天已签到"这类业务错误也无妨——测试只关心调用次数与
			// 批量结果结构，不关心上游语义。
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"OK","data":{}}`)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	p := pool.New("")
	for _, u := range uids {
		p.Add(&auth.Auth{UID: u, AccessToken: "at-" + u, RefreshToken: "rt-" + u, ExpiresAt: 9999999999})
	}
	pn := New(Config{Version: "test", APIKey: "panel-key", Pool: p, Upstream: up})
	return pn, &calls
}

// TestAccountsBatchDisableMulti 批量禁用：全部生效，逐账号结果齐全。
func TestAccountsBatchDisableMulti(t *testing.T) {
	pn, _ := batchPanel(t, "a", "b", "c")

	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", map[string]any{
		"action": "disable", "uids": []string{"a", "b"},
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool `json:"ok"`
		Total     int  `json:"total"`
		Succeeded int  `json:"succeeded"`
		Failed    int  `json:"failed"`
		Results   []struct {
			UID string `json:"uid"`
			OK  bool   `json:"ok"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 || resp.Succeeded != 2 || resp.Failed != 0 {
		t.Fatalf("resp=%+v want total=2 succeeded=2 failed=0", resp)
	}
	// 池内状态真的变了
	for _, uid := range []string{"a", "b"} {
		st, _ := pn.cfg.Pool.Status(uid)
		if !st.Disabled {
			t.Errorf("%s 未被禁用", uid)
		}
	}
	// 没被选中的 c 不受影响
	if st, _ := pn.cfg.Pool.Status("c"); st.Disabled {
		t.Error("c 未被选中却被禁用了")
	}
}

// TestAccountsBatchPartialFailure 部分失败不回滚：不存在的账号报错，其余照常执行。
//
// 这是批量操作最重要的语义：回滚会让"结果取决于顺序"，比部分失败更难理解。
func TestAccountsBatchPartialFailure(t *testing.T) {
	pn, _ := batchPanel(t, "a", "b")

	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", map[string]any{
		"action": "disable", "uids": []string{"a", "nope", "b"},
	}))
	var resp struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Results   []struct {
			UID   string `json:"uid"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Succeeded != 2 || resp.Failed != 1 {
		t.Fatalf("succeeded=%d failed=%d want 2/1", resp.Succeeded, resp.Failed)
	}
	// 失败的必须带原因，且能定位到是哪个 uid
	var failedOne bool
	for _, r := range resp.Results {
		if r.UID == "nope" {
			failedOne = true
			if r.OK || r.Error == "" {
				t.Errorf("不存在的账号应失败并带原因: %+v", r)
			}
		}
	}
	if !failedOne {
		t.Error("results 里没有 nope 这一项")
	}
	// 部分失败不影响已成功的两个
	for _, uid := range []string{"a", "b"} {
		if st, _ := pn.cfg.Pool.Status(uid); !st.Disabled {
			t.Errorf("%s 应已禁用（部分失败不该回滚）", uid)
		}
	}
}

// TestAccountsBatchDedupes 重复 uid 只执行一次（重复签到会白打上游）。
func TestAccountsBatchDedupes(t *testing.T) {
	pn, calls := batchPanel(t, "a", "b")

	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", map[string]any{
		"action": "keepalive", "uids": []string{"a", "a", " a ", "b"},
	}))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("total=%d want 2（a 重复 3 次应去重成 1 个）", resp.Total)
	}
	// 保活每账号一次上游刷新 → 2 次
	if got := *calls; got != 2 {
		t.Errorf("上游调用=%d want 2（去重后每账号一次）", got)
	}
}

// TestAccountsBatchRejectsBadInput 未知 action / 空 uids / 超上限都要 4xx。
func TestAccountsBatchRejectsBadInput(t *testing.T) {
	pn, _ := batchPanel(t, "a")
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"未知动作", map[string]any{"action": "explode", "uids": []string{"a"}}, 400},
		{"空 uids", map[string]any{"action": "disable", "uids": []string{}}, 400},
		{"只有空白", map[string]any{"action": "disable", "uids": []string{"  ", ""}}, 400},
		{"超上限", map[string]any{"action": "disable", "uids": manyUIDs(maxBatchAccounts + 1)}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", c.body))
			if rec.Code != c.want {
				t.Errorf("code=%d want %d body=%s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestAccountsBatchRequiresAuth 批量接口必须走面板鉴权（否则谁都能禁号/删号）。
func TestAccountsBatchRequiresAuth(t *testing.T) {
	pn, _ := batchPanel(t, "a")
	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, httptest.NewRequest("POST", "/panel/api/accounts/batch",
		strings.NewReader(`{"action":"disable","uids":["a"]}`)))
	if rec.Code != 401 {
		t.Fatalf("无鉴权 code=%d want 401", rec.Code)
	}
	if st, _ := pn.cfg.Pool.Status("a"); st.Disabled {
		t.Error("未鉴权请求改动了账号状态")
	}
}

// TestAccountsBatchReviveAndPause 解冻/暂停/恢复三个动作走同一端点，语义各自正确。
func TestAccountsBatchReviveAndPause(t *testing.T) {
	pn, _ := batchPanel(t, "a", "b")
	pn.cfg.Pool.Disable("a", "test")

	// 解冻
	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", map[string]any{
		"action": "revive", "uids": []string{"a"},
	}))
	if st, _ := pn.cfg.Pool.Status("a"); st.Disabled {
		t.Error("解冻未生效")
	}

	// 暂停
	rec = httptest.NewRecorder()
	pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", map[string]any{
		"action": "pause", "uids": []string{"a", "b"},
	}))
	for _, uid := range []string{"a", "b"} {
		if st, _ := pn.cfg.Pool.Status(uid); !st.Paused {
			t.Errorf("%s 未暂停", uid)
		}
	}

	// 恢复
	rec = httptest.NewRecorder()
	pn.ServeHTTP(rec, keyReq("POST", "/panel/api/accounts/batch", map[string]any{
		"action": "resume", "uids": []string{"a", "b"},
	}))
	for _, uid := range []string{"a", "b"} {
		if st, _ := pn.cfg.Pool.Status(uid); st.Paused {
			t.Errorf("%s 未恢复", uid)
		}
	}
}

// TestBatchActionNamesStable 错误提示里的动作清单必须稳定（测试与文档都依赖它）。
func TestBatchActionNamesStable(t *testing.T) {
	names := batchActionNames()
	if len(names) != len(batchActions) {
		t.Errorf("batchActionNames=%d 与 batchActions=%d 不一致", len(names), len(batchActions))
	}
	for _, n := range names {
		if _, ok := batchActions[n]; !ok {
			t.Errorf("batchActionNames 含未定义动作 %q", n)
		}
	}
}

// TestDedupeUIDs 去空白/去重/保序。
func TestDedupeUIDs(t *testing.T) {
	got := dedupeUIDs([]string{"b", " a ", "b", "", "  ", "c", "a"})
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupe=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupe=%v want %v（顺序也要保持）", got, want)
		}
	}
}

// manyUIDs 造 n 个不同 uid。
func manyUIDs(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "u"+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}
	return out
}
