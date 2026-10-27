// panel_gate_cache_test.go 全量接口防重入与模型目录短缓存护栏。
package panel

import (
	"testing"
	"time"
)

// TestBeginBatchExclusive 五个全量入口共用一个闸门：并发第二发被拒，结束后可再取。
func TestBeginBatchExclusive(t *testing.T) {
	p := &Panel{}
	if !p.beginBatch() {
		t.Fatal("首次应拿到闸门")
	}
	if p.beginBatch() {
		t.Fatal("在跑期间第二发必须被拒（409 语义）")
	}
	p.endBatch()
	if !p.beginBatch() {
		t.Fatal("释放后应可再次获取")
	}
	p.endBatch()
}

// TestModelsCacheTTL 缓存未过期命中、过期失效；空缓存不命中。
func TestModelsCacheTTL(t *testing.T) {
	old := modelsCacheTTL
	modelsCacheTTL = time.Minute
	defer func() { modelsCacheTTL = old }()

	p := &Panel{}
	if _, ok := p.cachedModels(); ok {
		t.Fatal("空缓存不应命中")
	}
	p.storeModels([]map[string]any{{"id": "m"}})
	got, ok := p.cachedModels()
	if !ok || len(got) != 1 {
		t.Fatalf("缓存未命中: ok=%v n=%d", ok, len(got))
	}

	// 过期：把写入时刻推回 TTL 之前。
	p.modelsMu.Lock()
	p.modelsCachedAt = time.Now().Add(-2 * time.Minute)
	p.modelsMu.Unlock()
	if _, ok := p.cachedModels(); ok {
		t.Error("过期缓存不应命中")
	}
}

// TestModelsCacheStoresOnlySuccess 部分失败不缓存（handler 只在 fetchErrs 为空时
// 调 storeModels）；这里钉住「缓存可被显式清除」的语义，避免一次抖动被冻结 60s。
func TestModelsCacheOverwrite(t *testing.T) {
	old := modelsCacheTTL
	modelsCacheTTL = time.Minute
	defer func() { modelsCacheTTL = old }()

	p := &Panel{}
	p.storeModels([]map[string]any{{"id": "old"}})
	p.storeModels([]map[string]any{{"id": "new"}})
	got, ok := p.cachedModels()
	if !ok || len(got) != 1 {
		t.Fatalf("ok=%v n=%d", ok, len(got))
	}
	if id, _ := got[0]["id"].(string); id != "new" {
		t.Errorf("后写应覆盖前值，得到 %q", id)
	}
}
