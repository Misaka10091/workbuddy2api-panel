// refresh_singleflight.go 刷新单飞与宽限复用。
//
// 背景：同一账号的 keepalive、请求前预刷新、面板探活会几乎同时发现 token 将过期，
// 各自打一次 refresh 就是拿**同一个 refresh token 花多次**。上游对 RT 有轮换，
// 并发双花让「后写覆盖先写」成为常态（client.go 的写回守卫只能防覆盖，防不住双花，
// 也防不住并发把最后一次轮换结果丢掉）。
//
// 本文件保证：
//   - 同 (账号, RT) 同时只有一个网络刷新，其余调用方等待并复用同一结果；
//   - 刷新成功后 60s 内，仍持有**旧** RT 快照的调用方直接复用结果，不再花第二次 RT；
//   - 失败结果不进宽限缓存（失败不该被复用，下次仍应真实重试）。
package upstream

import (
	"fmt"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// refreshGrace 刷新成功后结果的复用窗口（与官方 WorkBuddy 认证插件口径一致）。
var refreshGrace = 60 * time.Second

// refreshSweepAt 宽限表超过该条数时清理过期项（防止长期运行累积）。
const refreshSweepAt = 256

// refreshResult 一次刷新的结果（成功或失败），供在途等待者/宽限缓存复用。
type refreshResult struct {
	accessToken  string
	refreshToken string
	domain       string
	expiresAt    int64
	err          error
	at           time.Time
}

// refreshCall 一次在途刷新：done 关闭后 res 可读。
type refreshCall struct {
	done chan struct{}
	res  refreshResult
}

// refreshKey 按账号 + 当前 refresh token 标识一次刷新。RT 进 key 是为了让持有旧
// 快照的调用方与已换新的调用方区分开；UID 为空时回落 FilePath/指针（生产账号两者
// 都有值，测试可用 UID 固定 key）。
func refreshKey(a *auth.Auth, rt string) string {
	id := a.UID
	if id == "" {
		id = a.FilePath
	}
	if id == "" {
		id = fmt.Sprintf("%p", a)
	}
	return id + "|" + rt
}

// refreshCoalesced 单飞执行一次刷新，并把成功结果放进宽限缓存。
//
// 结果发布契约（等待者只等 done 关闭，不读别的同步原语）：
//   - leader 必须在 close(done) **之前**把结果写进 call.res。否则等待者读到的是
//     零值 refreshResult——err=nil——刷新失败会被并发等待者读成**成功**，它们于是
//     带着上游已拒绝的 token 继续打请求，误判随之扩散到冷却/禁用判定。
//   - leader 若在刷新路径 panic，仍要先发布一个明确失败并释放等待者，再把 panic
//     继续向上抛：等待者绝不能挂在无人 close 的 channel 上永久阻塞。
func (c *Client) refreshCoalesced(a *auth.Auth, rt string) error {
	key := refreshKey(a, rt)

	c.refreshMu.Lock()
	if res, ok := c.refreshRenewed[key]; ok {
		if time.Since(res.at) < refreshGrace {
			c.refreshMu.Unlock()
			res.applyTo(a, rt) // 复用结果同样要把新凭据交给调用方
			return res.err     // 宽限内复用：不再花第二次 RT
		}
		delete(c.refreshRenewed, key)
	}
	if call, ok := c.refreshInflight[key]; ok {
		c.refreshMu.Unlock()
		<-call.done // 等待在途刷新（同 key：同账号同 RT）
		// done 关闭前 leader 已发布 call.res（见上方契约）。
		call.res.applyTo(a, rt)
		return call.res.err
	}
	call := &refreshCall{done: make(chan struct{})}
	if c.refreshInflight == nil {
		c.refreshInflight = make(map[string]*refreshCall)
	}
	c.refreshInflight[key] = call
	c.refreshMu.Unlock()

	// 预置终止态：refreshOnce 若 panic，res 保持这个错误，等待者拿到的是明确失败
	// 而不是「零值 = 成功」。
	res := refreshResult{err: fmt.Errorf("refresh aborted: panic in refresh path")}
	defer func() {
		p := recover()
		c.releaseRefresh(key, call, res, rt)
		if p != nil {
			panic(p) // 等待者已释放，panic 继续向上传播（不吞崩溃）
		}
	}()

	res = c.refreshOnce(a)
	return res.err
}

// releaseRefresh 发布 leader 的结果并唤醒等待者。
//
// res 必须在 close(done) 之前落到 call.res 上：那是等待者唯一的读取点。
func (c *Client) releaseRefresh(key string, call *refreshCall, res refreshResult, rt string) {
	c.refreshMu.Lock()
	call.res = res
	delete(c.refreshInflight, key)
	// 只有**发生了 RT 轮换**的结果才进宽限缓存：宽限的语义是「后来者还拿着旧快照」，
	// 判据就是旧 RT 已被换掉（res.refreshToken != rt）。若上游没轮换 RT（响应省略
	// refreshToken 时本仓保留旧值），拿同一 RT 再来一次的调用方是**新鲜**调用而非
	// 陈旧快照，必须真实打上游——否则宽限窗口内会把「session 已失效」这类真实答复
	// 一并挡掉（keepalive 的连续 12153 计数就永远涨不上去）。
	if res.err == nil && res.refreshToken != "" && res.refreshToken != rt {
		if c.refreshRenewed == nil {
			c.refreshRenewed = make(map[string]refreshResult)
		}
		c.refreshRenewed[key] = res
		c.sweepRefreshLocked()
	}
	c.refreshMu.Unlock()

	close(call.done)
}

// applyTo 把一次**成功**刷新的结果写给等待者自己的 *auth.Auth。
//
// 为什么需要：单飞 key 是 (账号标识, RT)。同一账号在极端情形下可能有**不同**
// *auth.Auth 实例持有同一份 RT 快照（面板探活、重载、粘性快照各拿一份）。leader
// 只改了它自己那份；不把结果应用过去，等待者会「看到刷新成功」却仍带着旧 AT 请求。
//
// 只在等待者还持有发起时的那份 RT 时应用：期间若已被别人刷新（RT 已变），覆盖只会
// 把更新鲜的凭据写旧。响应省略的字段（refreshToken/domain/expiresAt）沿用旧值。
func (r refreshResult) applyTo(a *auth.Auth, staleRT string) {
	if r.err != nil || a == nil || r.accessToken == "" {
		return
	}
	a.Lock()
	defer a.Unlock()
	if a.RefreshToken != staleRT {
		return
	}
	a.AccessToken = r.accessToken
	if r.refreshToken != "" {
		a.RefreshToken = r.refreshToken
	}
	if r.domain != "" {
		a.Domain = r.domain
	}
	if r.expiresAt > 0 {
		a.ExpiresAt = r.expiresAt
	}
}

// sweepRefreshLocked 清理过期的宽限项（调用方持锁）。
func (c *Client) sweepRefreshLocked() {
	if len(c.refreshRenewed) <= refreshSweepAt {
		return
	}
	for k, v := range c.refreshRenewed {
		if time.Since(v.at) >= refreshGrace {
			delete(c.refreshRenewed, k)
		}
	}
}
