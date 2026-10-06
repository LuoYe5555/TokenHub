package provider

import (
	"bufio"
	"io"
	"strings"
)

// SSEEvent 一条解析后的 SSE 事件。
type SSEEvent struct {
	Event string // event: 行（可为空）
	Data  string // data: 行拼接
}

// ScanSSE 逐行解析 SSE 流（event:/data: 累积，空行分发）。
// fn 返回 false 时提前停止。
func ScanSSE(r io.Reader, fn func(ev SSEEvent) bool) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var ev SSEEvent
	flush := func() bool {
		if ev.Event == "" && ev.Data == "" {
			return true
		}
		ok := fn(ev)
		ev = SSEEvent{}
		return ok
	}
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "event:"):
			ev.Event = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		case strings.HasPrefix(trimmed, "data:"):
			if ev.Data != "" {
				ev.Data += "\n"
			}
			ev.Data += strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " ")
		case trimmed == "":
			if !flush() {
				return nil
			}
		// 注释行（:xxx）与 id:/retry: 忽略
		}
		if err != nil {
			// EOF 或读错误：把残余事件冲掉
			flush()
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
