package deepseek

import (
	"strings"
	"testing"

	"tokenhub/internal/provider"
)

// mockSink 收集 Delta/Finish 用于断言。
type mockSink struct {
	role      string
	reasoning strings.Builder
	content   strings.Builder
	finish    *provider.Finish
	err       string
}

func (m *mockSink) Delta(d provider.Delta) {
	if d.Role != "" {
		m.role = d.Role
	}
	m.reasoning.WriteString(d.Reasoning)
	m.content.WriteString(d.Content)
}
func (m *mockSink) Finish(f provider.Finish) { m.finish = &f }
func (m *mockSink) StreamError(_ int64, msg string) {
	m.err = msg
	if m.finish == nil {
		m.finish = &provider.Finish{Reason: "error"}
	}
}
func (m *mockSink) Close() {}

// 完整网页 SSE 流：ready → THINK 片段 → 裸增量 → RESPONSE 片段 → 裸增量 → FINISHED。
func TestParseSSEPatchFormat(t *testing.T) {
	stream := strings.Join([]string{
		"event: ready",
		`data: {"request_message_id":1,"response_message_id":2}`,
		"",
		`data: {"v":{"response":{"message_id":2,"fragments":[]}}}`,
		"",
		`data: {"p":"response/fragments","o":"APPEND","v":[{"type":"THINK","content":""}]}`,
		"",
		`data: {"p":"response/thinking_content","v":"先分析"}`,
		"",
		`data: {"v":"问题本质"}`,
		"",
		`data: {"p":"response/fragments","o":"APPEND","v":[{"type":"RESPONSE","content":""}]}`,
		"",
		`data: {"v":"数字取证"}`,
		"",
		`data: {"v":"是指运用科学"}`,
		"",
		`data: {"p":"response/status","o":"SET","v":"FINISHED"}`,
		"",
		"event: close",
		"",
	}, "\n")

	sink := &mockSink{}
	err := parseSSE(strings.NewReader(stream), sink, &parsedReq{content: "hi"})
	if err != nil {
		t.Fatalf("parseSSE error: %v", err)
	}
	if sink.reasoning.String() != "先分析问题本质" {
		t.Errorf("reasoning = %q", sink.reasoning.String())
	}
	if sink.content.String() != "数字取证是指运用科学" {
		t.Errorf("content = %q", sink.content.String())
	}
	if sink.finish == nil || sink.finish.Reason != "stop" {
		t.Errorf("finish = %+v", sink.finish)
	}
	if sink.finish != nil && sink.finish.Usage == nil {
		t.Errorf("usage 缺失")
	}
	if sink.err != "" {
		t.Errorf("意外错误: %s", sink.err)
	}
}

// hint 错误事件：未吐内容时按分类错误返回（池子可换号）。
func TestParseSSEHintError(t *testing.T) {
	stream := "event: hint\ndata: {\"type\":\"error\",\"content\":\"服务器繁忙，请稍后再试\"}\n\n"
	sink := &mockSink{}
	err := parseSSE(strings.NewReader(stream), sink, &parsedReq{content: "hi"})
	if err == nil {
		t.Fatalf("期待分类错误，得到 nil")
	}
	if pe, ok := err.(*provider.Error); !ok || pe.Kind != provider.KindRate {
		t.Errorf("错误类型不对: %v", err)
	}
}

// parseReq：模型名决定思考开关；多轮折叠。
func TestParseReqThinking(t *testing.T) {
	body := []byte(`{"model":"deepseek-reasoner","messages":[{"role":"system","content":"你是助手"},{"role":"user","content":"第一问"},{"role":"assistant","content":"第一答"},{"role":"user","content":"第二问"}]}`)
	req, err := parseReq(body, "deepseek-chat")
	if err != nil {
		t.Fatal(err)
	}
	if !req.thinking {
		t.Errorf("reasoner 应开思考")
	}
	if !strings.Contains(req.content, "第一答") || !strings.Contains(req.content, "第二问") || !strings.HasPrefix(req.content, "你是助手") {
		t.Errorf("折叠结果不对: %q", req.content)
	}

	req2, _ := parseReq([]byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`), "")
	if req2.thinking {
		t.Errorf("chat 应关思考")
	}
}
