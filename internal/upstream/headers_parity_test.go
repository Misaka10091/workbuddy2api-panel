// headers_parity_test.go 出站头族「形态与条件分支」断言（2026-10-28 同步 5.9.3，
// 上游 47f1ed6 的 headers_parity_test 同构移植）。
//
// 与 cn_contract_test.go 的分工：那边钉**键集**（哪条路径发哪些头，集合精确相等），
// 这边钉**形态与条件**（v7 时间位、id_source 条件头、根请求、agent-type、
// private-data 双分支、baggage 六键一致性）。两边都不能省：键集对了不代表值对，
// 值对了也不代表分支覆盖到了。
package upstream

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// uuidV7Re UUID v7 形态（第 13 位恒 7、变体位 8/9/a/b）。5.9.3 真机会话级 ID 的形态。
var uuidV7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// v7Millis 解出 UUID v7 的 48 bit 毫秒时间位（前 6 字节大端）。
func v7Millis(t *testing.T, id string) int64 {
	t.Helper()
	hexID := strings.ReplaceAll(id, "-", "")
	if len(hexID) != 32 {
		t.Fatalf("不是 32 hex：%q", id)
	}
	head, err := hex.DecodeString(hexID[:12])
	if err != nil {
		t.Fatalf("时间位不是 hex：%q", id)
	}
	var buf [8]byte
	copy(buf[2:], head)
	return int64(binary.BigEndian.Uint64(buf[:]))
}

// TestDerivedConversationIDsAreV7 派生分支（有粘性会话键、无客户端声明会话）必须发
// UUID v7，且同 (账号, 会话) 跨请求稳定、与会话 ID 不同值。
//
// 守护语义：5.9.3 真机的 X-Conversation-ID / acp-connection-id 是时间有序 v7；v4 在
// 第 13 位即可判别（上游 47f1ed6）。稳定性是缓存槽语义的前提——若时间位取
// time.Now()，同会话每轮换值，前缀缓存槽全废。
func TestDerivedConversationIDsAreV7(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	meta := ChatMeta{CacheKeyID: "sess-abc"}
	h1 := chatHeaderSet(t, a, meta)
	h2 := chatHeaderSet(t, a, meta)

	conv := h1["X-Conversation-Id"]
	if !uuidV7Re.MatchString(conv) {
		t.Fatalf("派生的 X-Conversation-ID 必须是 v7，得到 %q", conv)
	}
	if conv2 := h2["X-Conversation-Id"]; conv2 != conv {
		t.Errorf("同 (账号,会话) 派生值必须稳定：%q != %q", conv, conv2)
	}
	acp := h1["Acp-Connection-Id"]
	if !uuidV7Re.MatchString(acp) {
		t.Fatalf("acp-connection-id 必须是 v7，得到 %q", acp)
	}
	if acp == conv {
		t.Error("acp-connection-id 与会话 ID 必须不同值（官方两个值不同，但都按会话稳定）")
	}
	if h2["Acp-Connection-Id"] != acp {
		t.Error("acp-connection-id 必须跨请求稳定")
	}
}

// TestBuildTimeOrderedUUID 派生的 v7 形态/稳定性/隔离性 + 时间位落在窗口内。
//
// 守护语义：时间位是 sha256 派生的**伪时间**（锚 2026-01-01 + 24 个月窗口），
// 不是 time.Now()——否则同 (账号,会话) 每轮换值。窗口上界也断言，防止将来误把
// 越界值（如纳秒时间戳）塞进 48 bit。
func TestBuildTimeOrderedUUID(t *testing.T) {
	const (
		anchorMS = int64(1767225600000)                 // 2026-01-01T00:00:00Z
		windowMS = int64(24 * 30 * 24 * 60 * 60 * 1000) // ≈24 个月
	)
	got := buildTimeOrderedUUID("u1", "sess-1", "")
	if !uuidV7Re.MatchString(got) {
		t.Fatalf("形态不是 v7：%q", got)
	}
	if again := buildTimeOrderedUUID("u1", "sess-1", ""); again != got {
		t.Errorf("同输入必须恒同值（纯函数）：%q != %q", got, again)
	}
	if ms := v7Millis(t, got); ms < anchorMS || ms > anchorMS+windowMS {
		t.Errorf("时间位 %d 落在 [%d, %d] 之外", ms, anchorMS, anchorMS+windowMS)
	}
	// 隔离性：会话 / 用途 / 账号任一变化都必须换值。
	for _, other := range []string{
		buildTimeOrderedUUID("u1", "sess-2", ""),
		buildTimeOrderedUUID("u1", "sess-1", "acp"),
		buildTimeOrderedUUID("u2", "sess-1", ""),
	} {
		if other == got {
			t.Errorf("不同 (账号,会话,用途) 派生出同值 %q（隔离失效）", other)
		}
	}
	// prompt_cache_key 仍走 v4：这是有意的分界，改它会让存量缓存槽整体漂移。
	if v4 := buildCacheKey("u1", "sess-1"); uuidV7Re.MatchString(v4) {
		t.Errorf("prompt_cache_key 必须仍是 v4 形态，得到 %q", v4)
	}
}

// TestIdentityHeadersIdSource id_source 条件头：有该声明就发 X-Id-Source 且进
// X-Userinfo；**个人号**带 id_source 时不得出现 owner_uin / token_source。
//
// 守护语义（上游 47f1ed6 的关键修正）：token_source 描述 token 的来路，只在企业态
// 才是身份。旧实现「method 非空就发 X-Auth-Method」把个人号的来路当身份发了出去。
func TestIdentityHeadersIdSource(t *testing.T) {
	a := &auth.Auth{
		UID:         "u-1",
		AccessToken: jwtWithTwoClaims(map[string]string{"token_source": "enterprise_switch", "id_source": "sso"}),
	}
	h := chatHeaderSet(t, a, ChatMeta{})
	if got := h["X-Id-Source"]; got != "sso" {
		t.Fatalf("X-Id-Source=%q want sso", got)
	}
	if h["X-Auth-Method"] != "" {
		t.Errorf("个人号不得发 X-Auth-Method，得到 %q", h["X-Auth-Method"])
	}
	payload := decodeUserinfo(t, h["X-Userinfo"])
	if payload["id_source"] != "sso" {
		t.Errorf("X-Userinfo 应带 id_source: %v", payload)
	}
	if _, ok := payload["owner_uin"]; ok {
		t.Errorf("个人号不得带 owner_uin: %v", payload)
	}
	if _, ok := payload["token_source"]; ok {
		t.Errorf("个人号不得带 token_source: %v", payload)
	}
	if payload["uin"] != "u-1" {
		t.Errorf("X-Userinfo.uin=%v want u-1", payload["uin"])
	}

	// 企业号：id_source + token_source 都进 X-Userinfo，且发 X-Auth-Method。
	ent := &auth.Auth{
		UID:          "u-2",
		EnterpriseID: "ent-9",
		AccessToken:  jwtWithTwoClaims(map[string]string{"token_source": "enterprise_switch", "id_source": "sso"}),
	}
	he := chatHeaderSet(t, ent, ChatMeta{})
	if he["X-Auth-Method"] != "enterprise_switch" || he["X-Id-Source"] != "sso" {
		t.Errorf("企业号应同时发身份头：method=%q id_source=%q", he["X-Auth-Method"], he["X-Id-Source"])
	}
	pe := decodeUserinfo(t, he["X-Userinfo"])
	if pe["owner_uin"] != "ent-9" || pe["token_source"] != "enterprise_switch" || pe["id_source"] != "sso" {
		t.Errorf("企业号 X-Userinfo 三段不齐：%v", pe)
	}

	// 纯个人号（无 id_source）：三个身份头全不发。
	plain := chatHeaderSet(t, &auth.Auth{UID: "u-3", AccessToken: jwtWithClaim("token_source", "sso")}, ChatMeta{})
	for _, k := range []string{"X-Id-Source", "X-Auth-Method", "X-Userinfo"} {
		if plain[k] != "" {
			t.Errorf("纯个人号不得发 %s=%q", k, plain[k])
		}
	}
}

// TestRootRequestIDSessionScoped 根请求 ID 是**会话级**：与轮级 ID 不同值；
// 未提供时回落轮级（无会话概念调用方的既有形态）。
func TestRootRequestIDSessionScoped(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	h := chatHeaderSet(t, a, ChatMeta{
		ConversationID:        "conv-1",
		ConversationRequestID: "01a160b2c3d4e5f60718293a4b5c6d7e",
		RootRequestID:         "root0123456789abcdef0123456789abcd",
	})
	if got := h["X-Root-Request-Id"]; got != "root0123456789abcdef0123456789abcd" {
		t.Fatalf("X-Root-Request-ID=%q want 会话级根", got)
	}
	if h["X-Root-Request-Id"] == h["X-Conversation-Request-Id"] {
		t.Error("会话级根与轮级聚合主键必须不同值")
	}
	// 回落：未提供根时用轮级。
	fallback := chatHeaderSet(t, a, ChatMeta{ConversationRequestID: "01a160b2c3d4e5f60718293a4b5c6d7e"})
	if fallback["X-Root-Request-Id"] != "01a160b2c3d4e5f60718293a4b5c6d7e" {
		t.Errorf("无根时 X-Root-Request-ID 应回落轮级，得到 %q", fallback["X-Root-Request-Id"])
	}
	// 形态：32 hex（与轮级 ID 同形态），可直接作 B3 TraceId。
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(fallback["X-Root-Request-Id"]) {
		t.Errorf("根请求 ID 形态应为 32 hex，得到 %q", fallback["X-Root-Request-Id"])
	}
}

// TestAgentTypePerScene X-Agent-Type 缺省 main、显式 auxiliary。
//
// 守护语义：辅助链（面板判据对话 / 夜猫子）必须与用户真实对话区分，否则上游把网关
// 自发的后台会话也算进用户活跃（真机值域另有 compaction/subagent/team，网关不臆造）。
func TestAgentTypePerScene(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	if got := chatHeaderSet(t, a, ChatMeta{})["X-Agent-Type"]; got != AgentTypeMain {
		t.Errorf("缺省 X-Agent-Type=%q want %q", got, AgentTypeMain)
	}
	if got := chatHeaderSet(t, a, ChatMeta{AgentType: AgentTypeAuxiliary})["X-Agent-Type"]; got != "auxiliary" {
		t.Errorf("auxiliary 场景 X-Agent-Type=%q want auxiliary", got)
	}
}

// TestPrivateDataTrueBranch X-Private-Data 双分支：企业号 true（合规默认关模型优化）、
// 个人号 false（默认开优化）。
//
// 守护语义：5.9.3 真机反编译 `resolveModelOptimizationEnabled(...) ? "false" : "true"`。
// 判据是 EnterpriseID，不是 token_source——后者是 token 来路。
func TestPrivateDataTrueBranch(t *testing.T) {
	ent := &auth.Auth{AccessToken: "at", UID: "u1", EnterpriseID: "ent-9"}
	if got := chatHeaderSet(t, ent, ChatMeta{})["X-Private-Data"]; got != "true" {
		t.Errorf("企业号 X-Private-Data=%q want true", got)
	}
	personal := &auth.Auth{AccessToken: "at", UID: "u2"}
	if got := chatHeaderSet(t, personal, ChatMeta{})["X-Private-Data"]; got != "false" {
		t.Errorf("个人号 X-Private-Data=%q want false", got)
	}
	// 个人号即使带 token_source 也仍是 false（判据不看 token_source）。
	tricky := &auth.Auth{AccessToken: jwtWithClaim("token_source", "enterprise_switch"), UID: "u3"}
	if got := chatHeaderSet(t, tricky, ChatMeta{})["X-Private-Data"]; got != "false" {
		t.Errorf("个人号（带 token_source）X-Private-Data=%q want false", got)
	}
}

// TestBaggageCarriesRuntimeKeys baggage 六键：buddyId/buddyName 恒空但保留 k= 形态，
// codebuddy.session_id / codebuddy.conversation_request_id 与同名**头**同值，
// x-agent-runtime / x-agent-intent 与同名**头**同值。
//
// 守护语义：头体同值是硬要求（OTel propagator 序列化口径）；baggage 里的值一旦与头
// 分叉，上游会看到自相矛盾的链路上下文——比缺头更容易被判为非客户端形态。
func TestBaggageCarriesRuntimeKeys(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	h := chatHeaderSet(t, a, ChatMeta{
		ConversationID:        "conv-7",
		ConversationRequestID: "01a160b2c3d4e5f60718293a4b5c6d7e",
	})
	bag := h["Baggage"]
	if bag == "" {
		t.Fatal("baggage 必须注入（5.9.3 chat 黄金头集含该键）")
	}
	for _, want := range []string{
		"buddyId=",
		"buddyName=",
		"codebuddy.session_id=" + h["X-Conversation-Id"],
		"codebuddy.conversation_request_id=" + h["X-Conversation-Request-Id"],
		"x-agent-runtime=" + h["X-Agent-Runtime"],
		"x-agent-intent=" + h["X-Agent-Intent"],
	} {
		if !strings.Contains(bag, want) {
			t.Errorf("baggage 缺 %q：%s", want, bag)
		}
	}
	if h["X-Agent-Runtime"] != "local-agent" {
		t.Errorf("X-Agent-Runtime=%q want local-agent", h["X-Agent-Runtime"])
	}
	// 六键：以逗号分段计数（值内无逗号——都是 ID / 常量）。
	if n := len(strings.Split(bag, ",")); n != 6 {
		t.Errorf("baggage 应为 6 键，实际 %d 段：%s", n, bag)
	}
}

// jwtWithTwoClaims 造一枚带多个字符串声明的 JWT 形态 token（签名段不参与解析）。
func jwtWithTwoClaims(claims map[string]string) string {
	payload, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

// decodeUserinfo 解 X-Userinfo 的 base64(JSON) 段落。
func decodeUserinfo(t *testing.T, raw string) map[string]any {
	t.Helper()
	if raw == "" {
		t.Fatal("X-Userinfo 为空")
	}
	buf, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("X-Userinfo 不是合法 base64: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf, &payload); err != nil {
		t.Fatalf("X-Userinfo 不是合法 JSON: %v", err)
	}
	return payload
}
