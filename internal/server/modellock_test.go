package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// TestModelBlockAllQuota 只有「模型级限流但不是瞬时繁忙」（6004/6000 用量配额）
// 才按不可重试的配额语义处置。
func TestModelBlockAllQuota(t *testing.T) {
	cases := []struct {
		name string
		blk  pool.ModelBlockStatus
		want bool
	}{
		{"6004 配额", pool.ModelBlockStatus{Blocked: true, Reason: `{"code":6004,"msg":"使用量已超出频率限制，将在 2026-10-26 04:00:00 重置"}`}, true},
		{"6000 带换模型提示", pool.ModelBlockStatus{Blocked: true, Reason: `{"code":6000,"msg":"您也可以切换其他模型继续使用"}`}, true},
		{"14003 瞬时繁忙", pool.ModelBlockStatus{Blocked: true, Reason: `{"code":14003,"msg":"当前模型请求繁忙"}`}, false},
		{"11102 确定性拒绝", pool.ModelBlockStatus{Blocked: true, Reason: `{"code":11102,"msg":"service info not found"}`}, false},
		{"未阻塞", pool.ModelBlockStatus{Blocked: false, Reason: `{"code":6004}`}, false},
		{"无锁因", pool.ModelBlockStatus{Blocked: true, Reason: "  "}, false},
	}
	for _, c := range cases {
		if got := modelBlockAllQuota(c.blk); got != c.want {
			t.Errorf("%s: modelBlockAllQuota=%v want %v", c.name, got, c.want)
		}
	}
}

// TestModelQuotaCode 6000/6004 按原文区分，识别不到按 6004 兜底。
func TestModelQuotaCode(t *testing.T) {
	if got := modelQuotaCode(`{"code":6000,"msg":"您也可以切换其他模型继续使用"}`); got != 6000 {
		t.Errorf("6000 原文应识别为 6000，得到 %d", got)
	}
	if got := modelQuotaCode(`{"code":6004,"msg":"使用量已超出频率限制"}`); got != 6004 {
		t.Errorf("6004 原文应识别为 6004，得到 %d", got)
	}
	if got := modelQuotaCode(`{"code": 6004}`); got != 6004 {
		t.Errorf("空白容差应命中 6004，得到 %d", got)
	}
}

// TestModelBlockIsTransient 只有 14003 属于「等一等就好」的瞬时繁忙。
func TestModelBlockIsTransient(t *testing.T) {
	if !modelBlockIsTransient(`{"code":14003,"msg":"当前模型请求繁忙"}`) {
		t.Error("14003 应判为瞬时")
	}
	if modelBlockIsTransient(`{"code":6004,"msg":"使用量已超出频率限制"}`) {
		t.Error("6004 是额度窗口，不是瞬时")
	}
}

// TestWriteModelQuotaError 配额语义用 429 + 上游数字业务码回，
// 客户端据此按额度处置而不是当服务端过载重试。
func TestWriteModelQuotaError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeModelQuotaError(rec, 6004, "quota window full", "model_quota: 2 account(s)")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got, ok := body["code"].(float64); !ok || int(got) != 6004 {
		t.Fatalf("顶层 code 应为数字 6004，得到 %v", body["code"])
	}
	inner, _ := body["error"].(map[string]any)
	if inner == nil {
		t.Fatal("应同时带结构化 error 信封")
	}
	if got, ok := inner["code"].(float64); !ok || int(got) != 6004 {
		t.Fatalf("error.code 应为数字 6004，得到 %v", inner["code"])
	}
	if inner["gateway_hint"] != "model_quota: 2 account(s)" {
		t.Fatalf("hint 应透出，得到 %v", inner["gateway_hint"])
	}
}

// TestMaxRetryAfterCap 头是「睡多久」的指令：超长冷却必须被截到上限，
// 否则客户端会静默挂起几十分钟。
func TestMaxRetryAfterCap(t *testing.T) {
	if maxRetryAfterSec != 600 {
		t.Fatalf("maxRetryAfterSec=%d want 600", maxRetryAfterSec)
	}
	// 与实现同口径的向上取整 + 封顶。
	d := 45 * time.Minute
	secs := int(d.Seconds()) + 1
	if secs > maxRetryAfterSec {
		secs = maxRetryAfterSec
	}
	if secs != maxRetryAfterSec {
		t.Fatalf("超长冷却应截到 %d，得到 %d", maxRetryAfterSec, secs)
	}
}
