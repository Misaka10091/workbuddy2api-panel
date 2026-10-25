package upstream

import (
	"io"
	"strings"
	"testing"
)

// TestPeekFirstDataEventReplaysFullStream 窥探只读第一个 data 事件，
// 且返回的 Reader 必须能完整回放（否则下游会丢首帧内容）。
func TestPeekFirstDataEventReplaysFullStream(t *testing.T) {
	const stream = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	payload, rest, err := PeekFirstDataEvent(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !strings.Contains(payload, `"content":"hi"`) {
		t.Fatalf("payload=%q 应为首帧 data", payload)
	}
	raw, err := io.ReadAll(rest)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if string(raw) != stream {
		t.Fatalf("回放不完整:\n got=%q\nwant=%q", raw, stream)
	}
}

// TestPeekFirstDataEventErrorFrame 首帧是 error 帧时 payload 原样给出，
// 由调用方判定（peek 本身不分类）。
func TestPeekFirstDataEventErrorFrame(t *testing.T) {
	const stream = "data: {\"error\":{\"message\":\"upstream failed\"}}\n\ndata: [DONE]\n\n"
	payload, rest, err := PeekFirstDataEvent(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if FrameKind(payload) == ErrNone {
		t.Fatalf("payload=%q 应被判为 error 帧", payload)
	}
	if raw, _ := io.ReadAll(rest); string(raw) != stream {
		t.Fatalf("回放不完整: %q", raw)
	}
}

// TestPeekFirstDataEventEmptyStream 空流：payload 为空、错误透出、不 panic。
func TestPeekFirstDataEventEmptyStream(t *testing.T) {
	payload, rest, err := PeekFirstDataEvent(strings.NewReader(""))
	if payload != "" {
		t.Fatalf("空流不应有 payload，得到 %q", payload)
	}
	if err == nil {
		t.Log("空流在 EOF 处返回 err=nil 亦可（取决于底层 Reader 语义）")
	}
	if rest != nil {
		if raw, _ := io.ReadAll(rest); len(raw) != 0 {
			t.Fatalf("空流回放应为空，得到 %q", raw)
		}
	}
}

// TestPeekFirstDataEventSkipsCommentLines 注释/心跳行不算 data 事件，
// 应继续读到第一个 data（否则会把「: ping」当首帧而漏掉真错误）。
func TestPeekFirstDataEventSkipsCommentLines(t *testing.T) {
	const stream = ": ping\n\ndata: {\"error\":{\"message\":\"boom\"}}\n\n"
	payload, _, err := PeekFirstDataEvent(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !strings.Contains(payload, "boom") {
		t.Fatalf("应跳过注释行读到 data，得到 %q", payload)
	}
}
