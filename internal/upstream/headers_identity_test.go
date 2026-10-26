package upstream

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// jwtWithClaim 造一枚带指定字符串声明的 JWT 形态 access token（签名段不参与解析）。
func jwtWithClaim(name, value string) string {
	payload, _ := json.Marshal(map[string]any{name: value})
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

// TestIdentityHeadersEnterprise 企业号发 X-Auth-Method + X-Userinfo，
// 且 X-Userinfo 的段落按实际字段条件拼接。
func TestIdentityHeadersEnterprise(t *testing.T) {
	a := &auth.Auth{
		UID:          "u-1",
		EnterpriseID: "ent-9",
		AccessToken:  jwtWithClaim("token_source", "enterprise_switch"),
	}
	h := chatHeaderSet(t, a, ChatMeta{})
	if got := h["X-Auth-Method"]; got != "enterprise_switch" {
		t.Fatalf("X-Auth-Method=%q want enterprise_switch", got)
	}
	raw, err := base64.StdEncoding.DecodeString(h["X-Userinfo"])
	if err != nil {
		t.Fatalf("X-Userinfo 不是合法 base64: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("X-Userinfo 不是合法 JSON: %v", err)
	}
	if payload["uin"] != "u-1" || payload["owner_uin"] != "ent-9" {
		t.Fatalf("X-Userinfo 内容不对: %v", payload)
	}
	if payload["token_source"] != "enterprise_switch" {
		t.Fatalf("token_source 应随 method 一起出现: %v", payload)
	}
}

// TestIdentityHeadersPersonalSendsNone 个人号**零注入**——即使 token 带
// token_source（enterprise_switch）也不发：那是 token 的来路，不是账号身份。
func TestIdentityHeadersPersonalSendsNone(t *testing.T) {
	a := &auth.Auth{
		UID:         "u-2",
		AccessToken: jwtWithClaim("token_source", "enterprise_switch"),
	}
	h := chatHeaderSet(t, a, ChatMeta{})
	if h["X-Auth-Method"] != "" {
		t.Errorf("个人号不得发 X-Auth-Method，得到 %q", h["X-Auth-Method"])
	}
	if h["X-Userinfo"] != "" {
		t.Errorf("个人号不得发 X-Userinfo，得到 %q", h["X-Userinfo"])
	}
}

// TestIdentityHeadersEnterpriseWithoutTokenSource 企业号但 token 无 token_source：
// X-Auth-Method 不发，X-Userinfo 里也不得出现空串段。
func TestIdentityHeadersEnterpriseWithoutTokenSource(t *testing.T) {
	a := &auth.Auth{UID: "u-3", EnterpriseID: "ent-9", AccessToken: "not-a-jwt"}
	h := chatHeaderSet(t, a, ChatMeta{})
	if h["X-Auth-Method"] != "" {
		t.Errorf("无 token_source 时不得发 X-Auth-Method")
	}
	raw, err := base64.StdEncoding.DecodeString(h["X-Userinfo"])
	if err != nil {
		t.Fatalf("X-Userinfo 不是合法 base64: %v", err)
	}
	if strings.Contains(string(raw), `"token_source":""`) {
		t.Fatalf("缺字段不得写成空串段: %s", raw)
	}
	if !strings.Contains(string(raw), `"owner_uin":"ent-9"`) {
		t.Fatalf("企业号应带 owner_uin: %s", raw)
	}
}

// TestTokenSourceDecodesJWT auth.TokenSource 解 JWT 声明的口径。
func TestTokenSourceDecodesJWT(t *testing.T) {
	a := &auth.Auth{AccessToken: jwtWithClaim("token_source", "sso")}
	if got := a.TokenSource(); got != "sso" {
		t.Fatalf("TokenSource=%q want sso", got)
	}
	if got := (&auth.Auth{AccessToken: "plain"}).TokenSource(); got != "" {
		t.Fatalf("非 JWT 应返回空，得到 %q", got)
	}
	if got := (*auth.Auth)(nil).TokenSource(); got != "" {
		t.Fatalf("nil 应返回空，得到 %q", got)
	}
}
