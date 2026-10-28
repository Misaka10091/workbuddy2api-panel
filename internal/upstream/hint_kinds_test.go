package upstream

import (
	"strings"
	"testing"
)

// TestGatewayHintNewKinds 本轮新增的三个分类必须有对应 gateway_hint：
// 否则客户端只看到 400/503 原文，不知道该等、该换模型还是该新建会话。
func TestGatewayHintNewKinds(t *testing.T) {
	cases := []struct {
		name string
		kind ErrKind
		msg  string
		want string
	}{
		{
			name: "6021 队列满 → 网关已轮转、队列共享，按 Retry-After 等",
			kind: ErrUpstreamBusy,
			msg:  `{"code":6021,"msg":"queue.full.title"}`,
			want: "already rotated",
		},
		{
			name: "11148 配对断裂 → 新建会话",
			kind: ErrToolMismatch,
			msg:  `{"code":11148,"msg":"tool calls and tool results do not match"}`,
			want: "new conversation",
		},
		{
			name: "11133 兜底 → 参数被模型供应商拒绝",
			kind: ErrModelParamInvalid,
			msg:  `{"code":9999,"msg":"some provider rejection"}`,
			want: "parameters were rejected",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GatewayHint(c.kind, c.msg, HintContext{})
			if !strings.Contains(got, c.want) {
				t.Fatalf("hint=%q 应包含 %q", got, c.want)
			}
		})
	}
}

// TestGatewayHintParamInvalidWins 11133 家族仍优先走「模型不支持图片」判定，
// 不被新加的 ErrModelParamInvalid 兜底分支抢走。
func TestGatewayHintParamInvalidWins(t *testing.T) {
	ctx := HintContext{Model: "glm-5.3", HasImage: true, ModelInCatalog: true, ModelSupportsImages: false}
	got := GatewayHint(ErrModelParamInvalid, `{"code":11133,"msg":"model does not support image"}`, ctx)
	if !strings.Contains(got, "does not support images") {
		t.Fatalf("带图且目录声明不支持时 hint 应点名图片，得到 %q", got)
	}
}
