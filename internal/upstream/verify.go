package upstream

import (
	"errors"
	"net/http"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// VerifyVerdict 网关 401 的求证结论。
//
// 为什么需要这个枚举：网关 401 的响应体是 APISIX/openresty 的 HTML 页，
// **「token 真失效」与「瞬时拦截」的响应完全一样**，单看 401 分不出来。把判据
// 收敛到这里，任何新加的 401 处理都走同一套口径，不再各自发明。
type VerifyVerdict int

const (
	// VerifyAlive 会话仍有效：401 属瞬时拦截（风控 / 边缘抖动 / 并发限流），不该罚。
	VerifyAlive VerifyVerdict = iota
	// VerifyDead 登录态确实失效：refresh 被上游应用层明确拒绝，需重登。
	VerifyDead
	// VerifyInconclusive 无明确判词：网络 / 5xx / 429 / refresh 端点自身也被拦截。
	// 这种既不能判活也不能判死 —— 计入 session dead 就等于换个地方复发同一个误杀。
	VerifyInconclusive
)

func (v VerifyVerdict) String() string {
	switch v {
	case VerifyAlive:
		return "alive"
	case VerifyDead:
		return "dead"
	default:
		return "inconclusive"
	}
}

// SessionDeadButUsable 报告「refresh 判死，但账号此刻仍能用」。
//
// refresh token 失效与账号不可用是两件事：AT 未过期时上游 chat 仍正常。把这种号
// 计 session dead 会在连续保活后永久禁用一个完全健康的账号。判据不是上游文案，
// 也不是 JWT 的 azp，唯一稳定判据是 AT 是否还能用。
//
// 只适用于「AT 尚未被上游拒绝」的路径（保活 / 余额探测 / 发请求前的预刷新）；
// chat 路径拿到网关 401 时 AT 已被上游拒过，那里 refresh 判死就该计死。
func SessionDeadButUsable(a *auth.Auth) bool {
	return a != nil && !a.NeedsRefresh(0)
}

// VerifySessionByRefresh 用 refresh 求证会话是否仍有效（网关 401 的唯一可靠判据）。
//
// 判据：
//   - refresh 成功                        → VerifyAlive（会话活，401 只是那次被拦）
//   - refresh 被应用层明确拒绝（4xx 业务） → VerifyDead（token 真失效）
//   - 其它（网络错 / 5xx / 429 / refresh 端点自己也被 WAF 拦）→ VerifyInconclusive
//
// 第三条是关键：refresh 端点同样挂在 /v2/* 上，边缘拦截时它也回 HTML 页。把
// 「refresh 也失败」一律当死号，就等于在同一层再误杀一次。
//
// 调用方职责（本函数不做）：成功时落盘（上游会轮换 RT，不落盘重启即真死）、
// 按结论决定罚不罚。
func (c *Client) VerifySessionByRefresh(a *auth.Auth) VerifyVerdict {
	if c == nil || a == nil || a.RefreshTokenValue() == "" {
		return VerifyInconclusive
	}
	err := c.RefreshToken(a)
	if err == nil {
		return VerifyAlive
	}
	var re *Error
	if !errors.As(err, &re) {
		return VerifyInconclusive // 传输层/解析层错误：没有判词
	}
	// refresh 端点自身被拦截 / 上游抖动 / 限流：都不是「token 失效」的证据。
	if re.Status >= 500 || re.Status == http.StatusTooManyRequests ||
		IsGatewayUnauthorized(re.Status, re.Msg) || IsWafBlocked(re.Status, re.Msg) {
		return VerifyInconclusive
	}
	return VerifyDead
}
