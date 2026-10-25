package upstream

import (
	"encoding/json"
	"testing"
)

// TestNormalizeEffortPicksNearest 档位不支持时取**最近**档，而不是一律降级。
//
// 关键回归点（issue #175）：Codex 默认发 medium，CN 模型常见档位是 low/high/max
// （没有 medium）。旧的一律降级会落到 low——比不携带该字段时的模型默认档 high 还弱，
// 客户端完全无感。
func TestNormalizeEffortPicksNearest(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		req      string
		efforts  map[string][]string
		defaults map[string]string
		want     string
	}{
		{
			name:  "medium 无对应档 → 取默认档 high 而非 low",
			model: "glm-5.3",
			req:   "medium",
			efforts: map[string][]string{
				"glm-5.3": {"low", "high", "max"},
			},
			defaults: map[string]string{"glm-5.3": "high"},
			want:     "high",
		},
		{
			name:  "xhigh 无对应档 → 就近取 high",
			model: "glm-5.3",
			req:   "xhigh",
			efforts: map[string][]string{
				"glm-5.3": {"low", "high"},
			},
			want: "high",
		},
		{
			name:  "支持的档位原样透传",
			model: "glm-5.3",
			req:   "low",
			efforts: map[string][]string{
				"glm-5.3": {"low", "high"},
			},
			want: "low",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{
				"model":            c.model,
				"reasoning_effort": c.req,
				"messages":         []any{},
			})
			out := PrepareBodyOptWithEffortsAndDefault(raw, false, c.efforts, c.defaults)
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("出站 body 非法: %v", err)
			}
			if got, _ := obj["reasoning_effort"].(string); got != c.want {
				t.Fatalf("reasoning_effort=%q want %q", got, c.want)
			}
		})
	}
}

// TestNormalizeEffortHonorsOff 客户端显式关闭思考时，不得被就近降级成最弱的正档，
// 而是把该字段摘掉（off 不是上游认的档位）。
func TestNormalizeEffortHonorsOff(t *testing.T) {
	for _, req := range []string{"off", "none", "disabled"} {
		raw, _ := json.Marshal(map[string]any{
			"model":            "glm-5.3",
			"reasoning_effort": req,
			"messages":         []any{},
		})
		out := PrepareBodyOptWithEffortsAndDefault(raw, false,
			map[string][]string{"glm-5.3": {"low", "high", "max"}}, nil)
		var obj map[string]any
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatalf("出站 body 非法: %v", err)
		}
		if v, present := obj["reasoning_effort"]; present {
			t.Fatalf("off/none 应摘掉档位字段，得到 %v", v)
		}
	}
}
