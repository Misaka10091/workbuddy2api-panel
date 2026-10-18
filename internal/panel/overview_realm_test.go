package panel

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// TestOverviewExposesRealmCounts overview 必须透出 realm_counts（两域各自计数），
// 前端域卡片靠它渲染。这是「国内版 / 国际版分开管理」的数据入口。
func TestOverviewExposesRealmCounts(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn2", Domain: ""})
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})

	pn := New(Config{Version: "test", APIKey: "k", Pool: p})

	req := httptest.NewRequest("GET", "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("overview status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Accounts    []pool.Status               `json:"accounts"`
		RealmCounts map[string]pool.RealmCounts `json:"realm_counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse overview: %v\n%s", err, rec.Body.String())
	}

	if len(resp.RealmCounts) != 2 {
		t.Fatalf("realm_counts=%v want 2 个域（cn/global 恒在）", resp.RealmCounts)
	}
	if c := resp.RealmCounts["cn"]; c.Total != 2 || c.Healthy != 2 || !c.Servable {
		t.Errorf("cn counts=%+v want total=2 healthy=2 servable=true", c)
	}
	if g := resp.RealmCounts["global"]; g.Total != 1 || g.Healthy != 1 {
		t.Errorf("global counts=%+v want total=1 healthy=1", g)
	}
	// accounts 里每行也要带 realm：面板表格的分域标签与筛选都读它（缺了会被当 cn）。
	for _, a := range resp.Accounts {
		if a.Realm == "" {
			t.Errorf("account %s 缺 realm 字段（前端会把它当国内版）", a.UID)
		}
	}
}

// TestOverviewRealmCountsEmptyPool 空池也要返回两域的零值结构，
// 前端两张卡片无需判空（否则空池时整块不渲染，用户看不到「这里应该有什么」）。
func TestOverviewRealmCountsEmptyPool(t *testing.T) {
	pn := New(Config{Version: "test", APIKey: "k", Pool: pool.New("")})
	req := httptest.NewRequest("GET", "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, req)

	var resp struct {
		RealmCounts map[string]pool.RealmCounts `json:"realm_counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, realm := range []string{"cn", "global"} {
		c, ok := resp.RealmCounts[realm]
		if !ok {
			t.Fatalf("空池缺 %s 域结构", realm)
		}
		if c.Total != 0 || c.Servable {
			t.Errorf("%s=%+v want 全零", realm, c)
		}
	}
}
