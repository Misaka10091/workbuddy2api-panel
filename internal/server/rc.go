package server

import "io"

// readerWithCloser 把「已窥探并回放前缀的 Reader」与「原上游 body 的 Closer」组合成
// 一个 io.ReadCloser：读走回放流，Close 仍关到真正的上游响应体（否则连接泄漏）。
//
// 只用于 SSE 首帧窥探后继续透传的场景（见 upstream.PeekFirstDataEvent）。
type readerWithCloser struct {
	io.Reader
	io.Closer
}
