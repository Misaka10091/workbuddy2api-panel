// cn_contract_test.go CN 出站头族黄金集（2026-10-28 同步 WorkBuddy 5.9.3，上游 8ecf115
// 的 cn_contract_test 等价移植）。
//
// 口径（与上游一致）：取该路径**全部真机样本的交集**，剔除六类传输头
// （transportOnly）后逐键比对。断言三件事，缺一都算漂移：
//
//  1. 集合**精确相等**——多发一个客户端不发的头、漏发一个客户端必发的头都是红。
//     不用 strings.Contains 这类"包含"断言：那对「多一个头」完全无感。
//  2. values 逐键等值——同名不同值比多一个头更容易被规则命中。
//  3. absent 逐键为空——客户端在其它路径发、但本路径绝不发的头。
//
// 只建 CN 集：本仓 global 侧只有 /v2 单路径（chat 层无 fallback 链），但 UA 与 base
// 都按 realm 分叉，硬塞进同一份黄金集只会放大维护面。global 形态由
// ua_fingerprint_test.go / useragent_test.go 各自覆盖。
//
// values 里的 ${HOST} / ${HOSTNAME} 是运行期替换占位：X-Domain 的取值来自 base 解析，
// 而 base 在测试里是 httptest 的 127.0.0.1:PORT（chat/report 取 Hostname，
// v3-config 取 Host，见各自实现注释），没法写成静态字面量。
package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// transportOnly 传输层自动生成 / 不参与契约比对的头（上游 cn_contract_test 同口径）。
// x-device-token 单独剔除：它在真实账号上由凭证提供，而黄金集的判定对象是「客户端
// 固定形态」，避免测试依赖设备 token 来源；content-encoding 按 body 大小才加。
//
// 代价要说清：set 比对既然剔除了它，「某条路径**不该**带 X-Device-Token」就只剩
// absent 列表能守（见 report case）——而 absent 只有在账号真带设备令牌时才不是空转，
// 故下面的假账号显式带 DeviceToken。
var transportOnly = map[string]bool{
	"host":             true,
	"connection":       true,
	"content-length":   true,
	"accept-encoding":  true,
	"x-device-token":   true,
	"content-encoding": true,
}

// cnGolden 一条路径的黄金契约。values 支持 ${HOST}（host:port）与 ${HOSTNAME} 占位。
type cnGolden struct {
	name    string
	path    string
	headers []string          // 期望的键集（精确相等，小写）
	values  map[string]string // 期望的逐键取值
	absent  []string          // 绝不应出现的头
}

// cnRoundID 固定的轮级聚合主键（32 hex），让 X-Root-Request-ID 的回落值可断言。
const cnRoundID = "01a160b2c3d4e5f60718293a4b5c6d7e"

// cnGoldenCases 五条路径（chat / report / refresh / v3-config / billing）。
// 键数依次 38 / 9 / 11 / 8 / 7，与 5.9.3 真机样本交集一致。
// 上游的第六条 /v2/chat/busy 本仓没有该路径（无 ModelBusy），不建 case。
var cnGoldenCases = []cnGolden{
	{
		name: "chat",
		path: "/v2/chat/completions",
		headers: []string{
			"accept", "authorization", "acp-connection-id", "b3", "baggage", "content-type", "traceparent",
			"user-agent", "x-agent-intent", "x-agent-purpose", "x-agent-runtime", "x-agent-type",
			"x-b3-parentspanid", "x-b3-sampled", "x-b3-spanid", "x-b3-traceid", "x-codebuddy-request",
			"x-conversation-id", "x-conversation-message-id", "x-conversation-request-id", "x-domain",
			"x-ide-name", "x-ide-type", "x-ide-version", "x-private-data", "x-product", "x-request-id",
			"x-requested-with", "x-root-request-id", "x-trace-id", "x-user-id",
			"x-stainless-arch", "x-stainless-lang", "x-stainless-os", "x-stainless-package-version",
			"x-stainless-retry-count", "x-stainless-runtime", "x-stainless-runtime-version",
		},
		values: map[string]string{
			"accept":                      "application/json",
			"authorization":               "Bearer at",
			"content-type":                "application/json",
			"user-agent":                  "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0",
			"x-agent-intent":              "craft",
			"x-agent-purpose":             "conversation",
			"x-agent-runtime":             "local-agent",
			"x-agent-type":                "main",
			"x-b3-sampled":                "1",
			"x-codebuddy-request":         "1",
			"x-conversation-id":           "conv-42",
			"x-conversation-request-id":   cnRoundID,
			"x-domain":                    "${HOSTNAME}",
			"x-ide-name":                  "WorkBuddy",
			"x-ide-type":                  "WorkBuddy",
			"x-ide-version":               "5.9.3",
			"x-private-data":              "false",
			"x-product":                   "SaaS",
			"x-requested-with":            "XMLHttpRequest",
			"x-root-request-id":           cnRoundID, // 无会话级根 → 回落轮级
			"x-user-id":                   "u-1",
			"x-stainless-lang":            "js",
			"x-stainless-arch":            "x64",
			"x-stainless-os":              "Windows",
			"x-stainless-package-version": "6.25.0",
			"x-stainless-retry-count":     "0",
			"x-stainless-runtime":         "node",
			"x-stainless-runtime-version": "v22.21.1",
		},
		absent: []string{"origin", "referer", "accept-language", "x-machine-id", "x-session-id"},
	},
	{
		name: "report",
		path: "/v2/report",
		headers: []string{
			"accept", "authorization", "content-type", "user-agent", "x-domain", "x-product",
			"x-request-id", "x-requested-with", "x-user-id",
		},
		values: map[string]string{
			"accept":           "application/json, text/plain, */*",
			"authorization":    "Bearer at",
			"content-type":     "application/json;charset=UTF-8",
			"user-agent":       "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0",
			"x-domain":         "${HOSTNAME}",
			"x-product":        "SaaS",
			"x-requested-with": "XMLHttpRequest",
			"x-user-id":        "u-1",
		},
		// 真机 /v2/report 不带 X-Device-Token（只有 billing 族带）。它被 transportOnly
		// 从 set 比对里剔除，因此只能由 absent 守——账号显式带 DeviceToken 时才不是空转。
		absent: []string{"origin", "referer", "accept-language", "x-codebuddy-request", "x-device-token"},
	},
	{
		name: "refresh",
		path: "/v2/plugin/auth/token/refresh",
		headers: []string{
			"accept", "authorization", "content-type", "user-agent", "x-auth-refresh-source",
			"x-domain", "x-product", "x-refresh-token", "x-request-id", "x-requested-with", "x-user-id",
		},
		values: map[string]string{
			"accept":                "application/json, text/plain, */*",
			"authorization":         "Bearer at",
			"content-type":          "application/json",
			"user-agent":            "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0",
			"x-auth-refresh-source": "plugin",
			"x-domain":              "${HOSTNAME}",
			"x-product":             "SaaS",
			"x-refresh-token":       "rt",
			"x-requested-with":      "XMLHttpRequest",
			"x-user-id":             "u-1",
		},
		absent: []string{"origin", "referer", "accept-language", "x-codebuddy-request"},
	},
	{
		name: "v3-config",
		path: "/v3/config",
		headers: []string{
			"accept", "authorization", "user-agent", "x-domain", "x-product",
			"x-request-id", "x-requested-with", "x-user-id",
		},
		values: map[string]string{
			"accept":           "application/json, text/plain, */*",
			"authorization":    "Bearer at",
			"user-agent":       "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0",
			"x-domain":         "${HOST}",
			"x-product":        "SaaS",
			"x-requested-with": "XMLHttpRequest",
			"x-user-id":        "u-1",
		},
		absent: []string{"origin", "referer", "accept-language", "x-codebuddy-request"},
	},
	{
		name: "billing",
		path: "/v2/billing/meter/get-user-resource",
		headers: []string{
			"accept", "authorization", "content-type", "user-agent", "x-domain",
			"x-request-id", "x-user-id",
		},
		values: map[string]string{
			"accept":        "application/json",
			"authorization": "Bearer at",
			"content-type":  "application/json",
			"user-agent":    "axios/1.16.1",
			"x-domain":      "${HOSTNAME}",
			"x-user-id":     "u-1",
		},
		absent: []string{"origin", "referer", "accept-language", "x-codebuddy-request"},
	},
}

// cnContractHeaders 起一个假上游并**用生产函数真实发一遍**五条 CN 请求，
// 返回 srv 与 path → 上游收到的头。假上游按路径回最小合法信封（chat 回 SSE，
// 其余回 apiEnvelope）。
func cnContractHeaders(t *testing.T) (*httptest.Server, map[string]http.Header) {
	t.Helper()
	got := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got[r.URL.Path] = r.Header.Clone()
		switch r.URL.Path {
		case "/v2/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case "/v3/config":
			_, _ = w.Write([]byte(`{"code":0,"data":{"models":[{"id":"glm-5.2","name":"GLM"}]}}`))
		case "/v2/plugin/auth/token/refresh":
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"newat","refreshToken":"newrt","expiresIn":3600}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))

	c := &Client{
		HTTP:       srv.Client(),
		ChatHTTP:   srv.Client(),
		ChatBaseCN: srv.URL,
	}

	// chat
	// DeviceToken 显式给一个：chat/billing 族本来就带 X-Device-Token（transportOnly 已
	// 剔除，不影响 set 比对），而 report 的 absent 断言只有账号真带令牌时才可能被触发。
	chatAcct := &auth.Auth{AccessToken: "at", UID: "u-1", RefreshToken: "rt", DeviceToken: "dt"}
	rc, status, body, err := c.ChatStreamContext(context.Background(), chatAcct,
		[]byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{
			ConversationID:        "conv-42",
			ConversationRequestID: cnRoundID,
		})
	if rc != nil {
		_ = rc.Close()
	}
	if err != nil || status >= 400 {
		t.Fatalf("chat: status=%d err=%v body=%s", status, err, body)
	}

	// report（chatBase 通道）
	if err := c.ReportChatActivity(chatAcct, "conv-42", "req-42"); err != nil {
		t.Fatalf("report: %v", err)
	}

	// refresh（每次新建账号对象：刷新会改写 AT/RT/ExpiresAt）
	refreshAcct := &auth.Auth{AccessToken: "at", UID: "u-1", RefreshToken: "rt", ExpiresAt: 1}
	if err := c.RefreshToken(refreshAcct); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// v3-config（ua 空 → v3ProbeUA）
	if _, err := c.fetchV3ConfigModelMap(chatAcct, ""); err != nil {
		t.Fatalf("v3/config: %v", err)
	}

	// billing 族（BillingBaseCN 也指向同一假上游）
	c.BillingBaseCN = srv.URL
	if _, err := c.billingJSON(chatAcct, http.MethodPost, "/v2/billing/meter/get-user-resource", map[string]any{}); err != nil {
		t.Fatalf("billing: %v", err)
	}
	return srv, got
}

// TestCNOutboundGoldenHeaders 五条路径的头键集精确相等 + 取值等值 + 反向 absent。
func TestCNOutboundGoldenHeaders(t *testing.T) {
	srv, got := cnContractHeaders(t)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	hostname := host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		hostname = host[:i]
	}
	repl := strings.NewReplacer("${HOST}", host, "${HOSTNAME}", hostname)

	for _, tc := range cnGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			hdr, ok := got[tc.path]
			if !ok {
				t.Fatalf("上游没有收到 %s（生产函数未发到该路径）", tc.path)
			}
			// 1) 键集精确相等（剔除传输头）。
			have := map[string]bool{}
			for k := range hdr {
				lk := strings.ToLower(k)
				if transportOnly[lk] {
					continue
				}
				have[lk] = true
			}
			want := map[string]bool{}
			for _, k := range tc.headers {
				want[strings.ToLower(k)] = true
			}
			var extra, missing []string
			for k := range have {
				if !want[k] {
					extra = append(extra, k+"="+hdr.Get(k))
				}
			}
			for k := range want {
				if !have[k] {
					missing = append(missing, k)
				}
			}
			sort.Strings(extra)
			sort.Strings(missing)
			if len(extra) > 0 {
				t.Errorf("%s 多发头（客户端不发）: %v", tc.path, extra)
			}
			if len(missing) > 0 {
				t.Errorf("%s 漏发头（客户端必发）: %v", tc.path, missing)
			}
			// 2) 逐键取值。
			for k, wantV := range tc.values {
				if gotV := hdr.Get(k); gotV != repl.Replace(wantV) {
					t.Errorf("%s %s=%q want %q", tc.path, k, gotV, repl.Replace(wantV))
				}
			}
			// 3) 反向断言。
			for _, k := range tc.absent {
				if v := hdr.Get(k); v != "" {
					t.Errorf("%s 绝不应发 %s=%q", tc.path, k, v)
				}
			}
			// X-Domain 必须是裸主机名，不是带 scheme 的 URL（真机形态差异最明显的一处）。
			if d := hdr.Get("X-Domain"); strings.Contains(d, "://") {
				t.Errorf("%s X-Domain=%q 带 scheme", tc.path, d)
			}
		})
	}
}

// TestCNIDShapes 零值 meta 的 ID 形态：会话 ID / acp-connection-id 为 UUID v7，
// X-Request-ID 为 32 hex，消息级两头同值，X-Trace-ID 与轮级 ID 不同值。
//
// 守护语义：真机 5.9.3 的会话级 ID 是时间有序 v7（v4 在第 13 位即可判别）；
// 「X-Trace-ID 与 X-Request-ID 恒不相等」是 issue #35 的既有硬约束，不能因为换
// v7 而退回「两族相等」的自证形态。
func TestCNIDShapes(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u-1"}
	h := chatHeaderSet(t, a, ChatMeta{})

	if got := h["X-Conversation-Id"]; !uuidV7Re.MatchString(got) {
		t.Errorf("零值 X-Conversation-ID 必须是 v7，得到 %q", got)
	}
	if got := h["Acp-Connection-Id"]; !uuidV7Re.MatchString(got) {
		t.Errorf("零值 acp-connection-id 必须是 v7，得到 %q", got)
	}
	msg := h["X-Conversation-Message-Id"]
	if len(msg) != 32 || !isLowerHex(msg) {
		t.Errorf("X-Conversation-Message-ID 应为 32 hex，得到 %q", msg)
	}
	if h["X-Request-Id"] != msg {
		t.Errorf("X-Request-ID=%q 必须等于 X-Conversation-Message-ID=%q", h["X-Request-Id"], msg)
	}
	if h["X-Trace-Id"] == h["X-Conversation-Request-Id"] {
		t.Error("X-Trace-ID 不得等于 X-Conversation-Request-ID（两族恒不相等）")
	}
}

// TestCNRefreshBodyIsEmptyJSONObject refresh 的 body 是 2 字节 `{}`，不是空 body。
//
// 守护语义（PRD R1-7，上游 47f1ed6）：真机 refresh 的 Content-Length 恒 2；nil body
// 会发成 `Content-Length: 0`，是可直接判别的形态差。refresh 与 chat 相反——它**同时**
// 带 Authorization 与 X-Refresh-Token（那个由黄金头集守）。
func TestCNRefreshBodyIsEmptyJSONObject(t *testing.T) {
	var gotBody []byte
	var gotLen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotLen = r.Header.Get("Content-Length")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"newat","refreshToken":"newrt","expiresIn":3600}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatHTTP: srv.Client(), ChatBaseCN: srv.URL}
	a := &auth.Auth{AccessToken: "at", UID: "u-1", RefreshToken: "rt", ExpiresAt: 1}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if string(gotBody) != "{}" {
		t.Errorf("refresh body=%q want %q（真机 Content-Length 恒 2）", gotBody, "{}")
	}
	if gotLen != "2" {
		t.Errorf("refresh Content-Length=%q want 2", gotLen)
	}
}
