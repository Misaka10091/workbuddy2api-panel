package upstream

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestRefreshSingleFlight 并发刷新只允许花一次 RT：同 (账号, RT) 的在途刷新合并。
//
// 双花的现实路径：keepalive 定时器、请求前预刷新、面板探活同时判到过期。各自打一次
// refresh 会让上游多次轮换 RT，先写回的结果被后写覆盖，最坏情况把最后一次轮换丢掉。
func TestRefreshSingleFlight(t *testing.T) {
	var hits int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(20 * time.Millisecond) // 拉长窗口，保证后来者一定落在在途区间
		return jsonResp(200, `{"code":0,"data":{"accessToken":"newat","refreshToken":"newrt","expiresIn":3600}}`), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.RefreshToken(a)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发刷新返回错误: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("并发 %d 次刷新应只打 1 次上游，实际 %d 次（双花 RT）", n, got)
	}
	if a.AccessToken != "newat" || a.RefreshToken != "newrt" {
		t.Fatalf("token 未按刷新结果写回: %+v", a)
	}
}

// TestRefreshGraceReuse 刷新成功后 60s 内，仍持旧 RT 快照的调用方复用结果，
// 不再花第二次 RT。
func TestRefreshGraceReuse(t *testing.T) {
	var hits int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		return jsonResp(200, `{"code":0,"data":{"accessToken":"newat","refreshToken":"newrt","expiresIn":3600}}`), nil
	})
	first := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	if err := c.RefreshToken(first); err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}
	// 另一实例持有同一份旧 RT 快照（keepalive 与请求路径各持一份的典型形态）。
	stale := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	if err := c.RefreshToken(stale); err != nil {
		t.Fatalf("宽限复用返回错误: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("宽限内应复用结果，上游实际被打 %d 次", got)
	}
}

// TestRefreshGraceRequiresRotation 上游**没有轮换** RT 时不得宽限复用：
// 拿同一 RT 再来一次是新鲜调用（不是陈旧快照），必须真实打上游——否则宽限窗口内会把
// 「session 已失效」这类真实答复一起挡掉，保活的连续 12153 计数永远涨不上去。
func TestRefreshGraceRequiresRotation(t *testing.T) {
	var hits int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&hits, 1) == 1 {
			// 响应省略 refreshToken：本仓保留旧 RT（不轮换）。
			return jsonResp(200, `{"code":0,"data":{"accessToken":"new1","expiresIn":3600}}`), nil
		}
		return jsonResp(401, `{"code":12153,"msg":"Offline user session not found"}`), nil
	})
	a := &auth.Auth{UID: "u3", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}
	if err := c.RefreshToken(a); err == nil {
		t.Fatal("RT 未轮换时第二次刷新应真实打到上游并失败，而不是宽限复用")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("RT 未轮换不得宽限复用，上游应被打 2 次，实际 %d 次", got)
	}
}

// TestRefreshFailureNotCached 失败结果不进宽限缓存：下次仍应真实重试。
func TestRefreshFailureNotCached(t *testing.T) {
	var hits int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		return jsonResp(400, `{"code":12153,"msg":"Invalid refresh token"}`), nil
	})
	a := &auth.Auth{UID: "u2", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	if err := c.RefreshToken(a); err == nil {
		t.Fatal("预期 refresh 失败")
	}
	if err := c.RefreshToken(a); err == nil {
		t.Fatal("预期第二次 refresh 仍失败（失败不缓存）")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("失败不应被复用，上游应被打 2 次，实际 %d 次", got)
	}
}
