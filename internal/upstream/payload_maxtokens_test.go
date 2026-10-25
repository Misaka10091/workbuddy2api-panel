package upstream

import (
	"encoding/json"
	"testing"
)

// TestInjectCatalogMaxTokens 无显式限额时按目录 maxOutputTokens 补 max_tokens。
//
// 不补的代价：上游对缺失限额的请求套保守默认（实测 32000），不发限额的客户端
// （Codex 等）写大文件会被无声截断，且拿不到任何错误。
func TestInjectCatalogMaxTokens(t *testing.T) {
	caps := map[string]int64{"glm-5.3": 65536}

	t.Run("补目录值", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"model": "glm-5.3", "messages": []any{}})
		obj := decodeOutboundBody(t, PrepareBodyOptWithEffortsDefaultAndCaps(raw, false, nil, nil, caps))
		if got := obj["max_tokens"]; got != float64(65536) {
			t.Fatalf("max_tokens=%v want 65536", got)
		}
	})

	t.Run("显式限额优先", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"model": "glm-5.3", "max_tokens": 100, "messages": []any{}})
		obj := decodeOutboundBody(t, PrepareBodyOptWithEffortsDefaultAndCaps(raw, false, nil, nil, caps))
		if got := obj["max_tokens"]; got != float64(100) {
			t.Fatalf("显式 max_tokens 不应被覆盖，得到 %v", got)
		}
	})

	t.Run("目录无该模型不补", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"model": "unknown-model", "messages": []any{}})
		obj := decodeOutboundBody(t, PrepareBodyOptWithEffortsDefaultAndCaps(raw, false, nil, nil, caps))
		if _, present := obj["max_tokens"]; present {
			t.Fatalf("目录无该模型时不得臆造 max_tokens: %v", obj["max_tokens"])
		}
	})

	t.Run("realm 前缀剥离后查表", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"model": "global:glm-5.3", "messages": []any{}})
		obj := decodeOutboundBody(t, PrepareBodyOptWithEffortsDefaultAndCaps(raw, false, nil, nil, caps))
		if got := obj["max_tokens"]; got != float64(65536) {
			t.Fatalf("带前缀的模型应按裸名查到上限，得到 %v", got)
		}
	})

	t.Run("maxOut 为 nil 不改写", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"model": "glm-5.3", "messages": []any{}})
		obj := decodeOutboundBody(t, PrepareBodyOptWithEffortsAndDefault(raw, false, nil, nil))
		if _, present := obj["max_tokens"]; present {
			t.Fatalf("目录未知时不得补 max_tokens")
		}
	})
}

// TestClampStillWinsOverCatalog GPT 系过小的显式限额仍先被抬到下限：
// clamp 在目录补值之前跑，目录补值只填空缺，不改显式值。
func TestClampStillWinsOverCatalog(t *testing.T) {
	caps := map[string]int64{"gpt-6-sol": 32000}
	raw, _ := json.Marshal(map[string]any{"model": "gpt-6-sol", "max_tokens": 1, "messages": []any{}})
	obj := decodeOutboundBody(t, PrepareBodyOptWithEffortsDefaultAndCaps(raw, false, nil, nil, caps))
	if got := obj["max_tokens"]; got != float64(16) {
		t.Fatalf("过小显式值应被抬到 16（不是目录值），得到 %v", got)
	}
}
