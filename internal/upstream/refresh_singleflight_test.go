package upstream

import (
	"errors"
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
	// 复用结果必须把新凭据交给调用方：否则「复用成功」等于「白拿了个成功」，
	// 对方仍带着旧 AT 继续请求。
	if stale.AccessToken != "newat" || stale.RefreshToken != "newrt" {
		t.Fatalf("宽限复用的结果未应用到调用方: %+v", stale)
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

// TestRefreshCoalescedPublishesFailure 刷新失败必须原样传到并发等待者手里。
//
// 守护语义：等待者只等 done 关闭，读 call.res。leader 若不在 close 之前发布结果，
// 等待者读到的是零值 refreshResult（err=nil）——**失败被读成成功**，于是并发等待者
// 带着上游已拒绝的 token 继续请求，后续误判会扩散到冷却/禁用。把发布语句去掉，
// 本测试必须失败（这条就是它存在的唯一理由）。
func TestRefreshCoalescedPublishesFailure(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var hits int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&hits, 1) == 1 {
			close(entered)
			<-release // 把 leader 扣在刷新路径里，等等待者加入在途集合
		}
		return jsonResp(400, `{"code":12153,"msg":"Invalid refresh token"}`), nil
	})

	leader := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}
	waiter := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}

	leaderErr := make(chan error, 1)
	go func() { leaderErr <- c.RefreshToken(leader) }()
	<-entered

	waiterErr := make(chan error, 1)
	go func() { waiterErr <- c.RefreshToken(waiter) }()
	// 等一小会儿：让等待者先进入在途分支。leader 仍被扣在 transport 里，不可能
	// 已经返回；即使这里没赶上，下面「失败不能变成功」的断言依然成立。
	time.Sleep(30 * time.Millisecond)
	close(release)

	for i, ch := range []chan error{leaderErr, waiterErr} {
		select {
		case err := <-ch:
			if err == nil {
				t.Fatalf("第 %d 个调用方把刷新失败读成了成功（nil error）", i+1)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 个调用方被永久阻塞（done 未关闭）", i+1)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("同 (账号, RT) 应单飞，上游实际被打 %d 次 want 1", got)
	}
}

// TestRefreshPanicReleasesWaiters 刷新路径 panic 时等待者不得永久阻塞：
// leader 先发布明确失败并关闭 done，再把 panic 继续向上抛。
//
// transport 里的 panic 会穿过 doJSON 冒到 refreshOnce，正是这条路径。
func TestRefreshPanicReleasesWaiters(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
			panic("boom in transport")
		}
		return nil, errors.New("transport down")
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}

	leaderPanicked := make(chan any, 1)
	go func() {
		defer func() { leaderPanicked <- recover() }()
		_ = c.RefreshToken(a)
	}()
	<-entered

	// 等待者与 leader 同 key，会挂在 <-call.done 上。
	waiterErr := make(chan error, 1)
	go func() { waiterErr <- c.RefreshToken(a) }()
	// 等一小会儿让等待者先进在途分支；leader 仍被扣在 transport 里，未返回。
	time.Sleep(30 * time.Millisecond)
	close(release)

	select {
	case p := <-leaderPanicked:
		if p == nil {
			t.Fatal("transport panic 应向上传播，不得被吞掉")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader 在 panic 路径上挂死")
	}

	select {
	case err := <-waiterErr:
		if err == nil {
			t.Fatal("leader panic 后等待者不得把结果读成成功")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader panic 后等待者被永久阻塞（done 未关闭）")
	}

	c.refreshMu.Lock()
	left := len(c.refreshInflight)
	c.refreshMu.Unlock()
	if left != 0 {
		t.Fatalf("panic 后 inflight 未清理：残留 %d 条", left)
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
