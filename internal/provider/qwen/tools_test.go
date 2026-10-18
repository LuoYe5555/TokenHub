package qwen

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolCallStreamParserSplit(t *testing.T) {
	p := newToolCallParser(map[string]bool{"Read": true})
	// tag 被切成两半跨 chunk
	out1, calls1 := p.push("你好 <tool_")
	if out1 != "你好 " || len(calls1) != 0 {
		t.Fatalf("chunk1: out=%q calls=%v", out1, calls1)
	}
	out2, calls2 := p.push("call>\n{\"name\": \"Read\", \"arguments\": {\"path\": \"a.go\"}}\n</tool_call> 完")
	if out2 != " 完" || len(calls2) != 1 {
		t.Fatalf("chunk2: out=%q calls=%v", out2, calls2)
	}
	if calls2[0].Name != "Read" || calls2[0].Index != 0 {
		t.Fatalf("call: %+v", calls2[0])
	}
	var args map[string]any
	if json.Unmarshal([]byte(calls2[0].Args), &args) != nil || args["path"] != "a.go" {
		t.Fatalf("args: %s", calls2[0].Args)
	}
	tail, calls3 := p.flush()
	if tail != "" || len(calls3) != 0 {
		t.Fatalf("flush: tail=%q calls=%v", tail, calls3)
	}
}

func TestToolCallStreamParserMulti(t *testing.T) {
	p := newToolCallParser(map[string]bool{"Glob": true})
	_, calls := p.push(strings.Repeat("<tool_call>{\"name\":\"Glob\"}</tool_call>", 3))
	if len(calls) != 3 {
		t.Fatalf("expect 3 calls from push, got %d", len(calls))
	}
	for i, c := range calls {
		if c.Index != i || c.Name != "Glob" {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
	tail, calls2 := p.flush()
	if tail != "" || len(calls2) != 0 {
		t.Fatalf("flush should be empty: tail=%q calls=%v", tail, calls2)
	}
}

func TestToolCallStreamParserUnclosed(t *testing.T) {
	p := newToolCallParser(map[string]bool{"Bash": true})
	p.push("<tool_call>{\"name\": \"Bash\", \"arguments\": {}}")
	tail, calls := p.flush()
	if len(calls) != 1 || calls[0].Name != "Bash" {
		t.Fatalf("unclosed call not recovered: %+v", calls)
	}
	if tail != "" {
		t.Fatalf("unexpected tail %q", tail)
	}
}

func TestToolCallStreamParserUnknownNameRejected(t *testing.T) {
	// 千问幻觉出列表外工具名（如 Task/LS）：不得转发，转成提示文本
	p := newToolCallParser(map[string]bool{"Read": true, "Grep": true})
	out, calls := p.push(`<tool_call>{"name": "Task", "arguments": {}}</tool_call>ok`)
	if len(calls) != 0 {
		t.Fatalf("unknown name must not forward: %+v", calls)
	}
	if !strings.Contains(out, `"Task"`) || !strings.Contains(out, "Grep, Read") {
		t.Fatalf("reject note missing: %q", out)
	}
	// 白名单内的照常转发
	_, calls2 := p.push(`<tool_call>{"name": "Read", "arguments": {"path":"x"}}</tool_call>`)
	if len(calls2) != 1 || calls2[0].Name != "Read" {
		t.Fatalf("known call lost: %+v", calls2)
	}
}

func TestToolCallStreamParserNoTools(t *testing.T) {
	// 请求未带 tools：任何调用都不转发，仅提示
	p := newToolCallParser(nil)
	out, calls := p.push(`<tool_call>{"name": "Read", "arguments": {}}</tool_call>`)
	if len(calls) != 0 {
		t.Fatalf("no-tools session must not forward calls: %+v", calls)
	}
	if !strings.Contains(out, "no tools are enabled") {
		t.Fatalf("note missing: %q", out)
	}
}

func TestBuildToolSystemPrompt(t *testing.T) {
	tools := []json.RawMessage{
		json.RawMessage(`{"type":"function","function":{"name":"Read","description":"读文件","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}`),
	}
	s := buildToolSystemPrompt(tools, false)
	if !strings.Contains(s, "Read") || !strings.Contains(s, tcOpen) {
		t.Fatalf("prompt missing parts: %s", s)
	}
	if buildToolSystemPrompt(nil, false) != "" {
		t.Fatal("empty tools should yield empty prompt")
	}
}

func TestParseReqWithTools(t *testing.T) {
	req := []byte(`{
		"model": "qwen:qwen3.8-max",
		"tools": [{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],
		"messages": [
			{"role": "system", "content": "be nice"},
			{"role": "user", "content": "list files"},
			{"role": "assistant", "tool_calls": [{"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"a.go\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "name": "Read", "content": "file body"}
		]
	}`)
	pr, err := parseReq(req, "qwen3.8-max")
	if err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"Available Tools", "Read", tcOpen, "<tool_response", `{"path":"a.go"}`} {
		if !strings.Contains(pr.content, kw) {
			t.Fatalf("content missing %q:\n%s", kw, pr.content)
		}
	}
}

func TestParseToolCallPayloadVariants(t *testing.T) {
	name, args := parseToolCallPayload("{\"tool\": \"Foo\", \"parameters\": {\"x\": 1}}")
	if name != "Foo" || string(args) != `{"x":1}` {
		t.Fatalf("alias: %q %s", name, args)
	}
	name2, _ := parseToolCallPayload("```json\n{\"name\": \"Bar\"}\n```")
	if name2 != "Bar" {
		t.Fatalf("fence: %q", name2)
	}
	if n, _ := parseToolCallPayload("not json"); n != "" {
		t.Fatalf("should be empty: %q", n)
	}
}
