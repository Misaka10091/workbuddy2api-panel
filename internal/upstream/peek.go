// peek.go SSE 首帧窥探：用于「上游 200 开流、第一帧就是 error 帧」的零字节否决。
//
// 背景：上游对限流/内容拦截/审核失败，会先回 200 开流，再把错误作为一帧 error 事件
// 发下来。字节一旦写出，网关就只能给客户端 200 + error 帧——很多客户端把这种响应当
// 成「空成功」，既不重试也不报错。首帧阶段其实一个字节都没写，此时否决并换号，客户端
// 拿到的是另一号的成功流（或轮转耗尽后的真实错误码）。
package upstream

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// PeekFirstDataEvent 读取第一个 `data:` 事件，返回其 payload 与一个「已读回放 + 余下」
// 的 Reader（调用方必须用返回的 Reader 继续读，不能再用原 Reader：bufio 可能已越过事件
// 边界预读）。
//
// 语义边界：
//   - 只读**第一个** data 事件，读完即停（不解析 JSON，不判断是不是 error——判定交给调用方）；
//   - 读到 EOF 都没出现 data 行（空流/纯注释流）→ payload 为空、err 透出底层错误；
//   - rest 始终包含所有已消费字节的回放，因此无论是否否决，流内容都不丢。
func PeekFirstDataEvent(r io.Reader) (payload string, rest io.Reader, err error) {
	if r == nil {
		return "", nil, nil
	}
	br := bufio.NewReaderSize(r, 64*1024)
	var consumed bytes.Buffer
	replay := func() io.Reader {
		return io.MultiReader(bytes.NewReader(consumed.Bytes()), br)
	}
	for {
		line, readErr := br.ReadString('\n')
		consumed.WriteString(line)
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			// 吃掉本事件的剩余行，直到空行：保证 rest 从下一个事件边界开始，
			// 不会把同一事件切成两半再交给下游解析器。
			for {
				l, e2 := br.ReadString('\n')
				consumed.WriteString(l)
				if strings.TrimRight(l, "\r\n") == "" {
					break
				}
				if e2 != nil {
					break
				}
			}
			return payload, replay(), nil
		}
		if readErr != nil {
			// 没读到 data 就结束：原样回放，错误透出（空流由调用方按既有权处理）。
			return "", replay(), readErr
		}
	}
}
