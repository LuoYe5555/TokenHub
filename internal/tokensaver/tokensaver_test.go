package tokensaver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func mustParse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("优化结果不是合法 JSON: %v", err)
	}
	return m
}

// JSON 紧凑化：压平 pretty JSON，字符串值逐字节保留
func TestCompactJSON(t *testing.T) {
	pretty := "{\n  \"results\": [\n    {\"name\": \"test_a<b&c\", \"pass\": true}\n  ]\n}"
	body := []byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":"1","content":` + quoteJSON(pretty) + `}]}`)
	out, res := Optimize(body, Options{CompactJSON: true})
	if res.Compacted != 1 || res.SavedChars <= 0 {
		t.Fatalf("期望紧凑化 1 处, got %+v", res)
	}
	m := mustParse(t, out)
	msg := m["messages"].([]any)[0].(map[string]any)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(msg["content"].(string)), &parsed); err != nil {
		t.Fatalf("紧凑化破坏了 JSON: %v", err)
	}
	item := parsed["results"].([]any)[0].(map[string]any)
	if item["name"] != "test_a<b&c" {
		t.Fatalf("字符串值被改动: %q", item["name"])
	}
	if strings.Contains(msg["content"].(string), "\n") {
		t.Fatal("仍含换行，未压平")
	}
	// HTML 转义必须关闭：原始字符不应变 \u003c
	if strings.Contains(string(out), "\\u003c") {
		t.Fatal("发生了 HTML 转义膨胀")
	}
}

// 非 JSON 内容不动；不完整的 JSON 不动
func TestCompactSkipsNonJSON(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"{\n  这不是JSON\n}"}]}`)
	_, res := Optimize(body, Options{CompactJSON: true})
	if res.Compacted != 0 {
		t.Fatalf("非法 JSON 不应被改: %+v", res)
	}
}

// tool_call 参数紧凑化
func TestCompactToolArgs(t *testing.T) {
	args := "{\n  \"path\": \"/a/b.go\",\n  \"content\": \"line1\\nline2\"\n}"
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write","arguments":` + quoteJSON(args) + `}}]}]}`)
	out, res := Optimize(body, Options{CompactJSON: true})
	if res.Compacted != 1 {
		t.Fatalf("期望紧凑化参数: %+v", res)
	}
	m := mustParse(t, out)
	tc := m["messages"].([]any)[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	var p map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &p); err != nil {
		t.Fatal(err)
	}
	if p["content"] != "line1\nline2" || p["path"] != "/a/b.go" {
		t.Fatalf("参数字符串值被改动: %v", p)
	}
}

// 重复折叠：保留最新一份，旧变标记；短内容不折叠；system 不动
func TestDedup(t *testing.T) {
	big := strings.Repeat("x", 600)
	mk := func(id string) string {
		return `{"messages":[` +
			`{"role":"system","content":` + quoteJSON(big) + `},` +
			`{"role":"tool","tool_call_id":"` + id + `","content":` + quoteJSON(big) + `},` +
			`{"role":"assistant","content":"ok"},` +
			`{"role":"user","content":` + quoteJSON(big) + `},` +
			`{"role":"tool","tool_call_id":"last","content":` + quoteJSON(big) + `}]}` + "\n"
	}
	out, res := Optimize([]byte(mk("t1")), Options{Dedup: true, MinChars: 500})
	if res.Deduped != 2 {
		t.Fatalf("期望折叠 2 处(tool+user，system 不动，最后一份保留): %+v", res)
	}
	m := mustParse(t, out)
	msgs := m["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != big {
		t.Fatal("system 被改动了")
	}
	if msgs[1].(map[string]any)["content"] != dedupMark {
		t.Fatal("旧 tool 内容未折叠")
	}
	if msgs[3].(map[string]any)["content"] != dedupMark {
		t.Fatal("旧 user 内容未折叠")
	}
	if msgs[4].(map[string]any)["content"] != big {
		t.Fatal("最新一份必须完整保留")
	}
	if !strings.Contains(string(out), `"tool_call_id":"last"`) {
		t.Fatal("消息结构被破坏")
	}
}

// 小内容不折叠
func TestDedupMinChars(t *testing.T) {
	s := strings.Repeat("x", 100)
	body := []byte(`{"messages":[{"role":"tool","tool_call_id":"a","content":` + quoteJSON(s) + `},{"role":"tool","tool_call_id":"b","content":` + quoteJSON(s) + `}]}`)
	_, res := Optimize(body, Options{Dedup: true, MinChars: 500})
	if res.Deduped != 0 {
		t.Fatalf("短内容不应折叠: %+v", res)
	}
}

// 大整数不被 float64 破坏
func TestBigNumbersPreserved(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":9007199254740993,"messages":[{"role":"user","content":"hi"}]}`)
	out, _ := Optimize(body, Options{CompactJSON: true})
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("大整数被破坏: %s", out)
	}
}

// 非法请求体原样返回
func TestInvalidBody(t *testing.T) {
	bad := []byte("not json at all")
	out, res := Optimize(bad, Options{Dedup: true, CompactJSON: true})
	if res.SavedChars != 0 || string(out) != string(bad) {
		t.Fatal("非法输入必须原样返回")
	}
}

// 数组形式 content（单 text 块）也能处理
func TestArrayContent(t *testing.T) {
	big := strings.Repeat("y", 600)
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":` + quoteJSON(big) + `}]},{"role":"user","content":[{"type":"text","text":` + quoteJSON(big) + `}]}]}`)
	out, res := Optimize(body, Options{Dedup: true})
	if res.Deduped != 1 {
		t.Fatalf("数组 content 应可折叠: %+v", res)
	}
	m := mustParse(t, out)
	first := m["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if first["text"] != dedupMark {
		t.Fatal("数组 content 折叠失败（第一份应为标记）")
	}
	second := m["messages"].([]any)[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if second["text"] != big {
		t.Fatal("最新一份必须完整保留")
	}
}

// 空数组 content / 多块 content 不动
func TestMultiBlockSkipped(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`)
	_, res := Optimize(body, Options{Dedup: true, CompactJSON: true})
	if res.SavedChars != 0 {
		t.Fatal("多块 content 不应处理")
	}
}

// 开关全关时原样返回
func TestDisabled(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, _ := Optimize(body, Options{})
	if string(out) != strings.TrimRight(string(body), "\n") && string(out) != string(body) {
		// 全关时 Optimize 在最前面就返回原 body
		t.Fatalf("全关时应原样返回: %s", out)
	}
}

// 优化后模型等顶层字段保留
func TestTopLevelFieldsKept(t *testing.T) {
	body := []byte(`{"model":"tr:glm-5.2","stream":true,"temperature":0.7,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`)
	out, _ := Optimize(body, Options{Dedup: true, CompactJSON: true})
	m := mustParse(t, out)
	if m["model"] != "tr:glm-5.2" || m["stream"] != true || m["temperature"] != 0.7 {
		t.Fatalf("顶层字段丢失: %v", m)
	}
	if _, ok := m["tools"]; !ok {
		t.Fatal("tools 字段丢失")
	}
}

// 过期工具结果淘汰：同工具同参多次调用只保留最新，参数键序差异视为相同
func TestStaleToolResults(t *testing.T) {
	old := strings.Repeat("A", 500) // 文件旧版本
	mid := strings.Repeat("B", 500) // 改动后的版本
	latest := strings.Repeat("C", 500)
	args1 := `{"path":"main.go","line":10}`
	args2 := `{"line":10,"path":"main.go"}` // 键序不同，内容相同
	body := []byte(`{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":` + quoteJSON(args1) + `}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(old) + `},` +
		`{"role":"assistant","tool_calls":[{"id":"c2","type":"function","function":{"name":"read_file","arguments":` + quoteJSON(args2) + `}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":` + quoteJSON(mid) + `},` +
		`{"role":"assistant","tool_calls":[{"id":"c3","type":"function","function":{"name":"read_file","arguments":` + quoteJSON(args1) + `}}]},` +
		`{"role":"tool","tool_call_id":"c3","content":` + quoteJSON(latest) + `}]}`)
	out, res := Optimize(body, Options{Dedup: true})
	if res.Staled != 2 {
		t.Fatalf("期望淘汰 2 份过期结果: %+v", res)
	}
	msgs := mustParse(t, out)["messages"].([]any)
	if msgs[1].(map[string]any)["content"] != staleMark || msgs[3].(map[string]any)["content"] != staleMark {
		t.Fatal("旧结果未被淘汰为标记")
	}
	if msgs[5].(map[string]any)["content"] != latest {
		t.Fatal("最新结果必须保留")
	}
}

// 不同参数的工具结果不互相淘汰；短结果不淘汰
func TestStaleSkips(t *testing.T) {
	a := strings.Repeat("A", 400)
	b := strings.Repeat("B", 400)
	body := []byte(`{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(a) + `},` +
		`{"role":"assistant","tool_calls":[{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":` + quoteJSON(b) + `}]}`)
	if _, res := Optimize(body, Options{Dedup: true}); res.Staled != 0 {
		t.Fatalf("不同参数不应淘汰: %+v", res)
	}
	short := strings.Repeat("s", 100)
	body2 := []byte(`{"messages":[` +
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(short) + `},` +
		`{"role":"assistant","tool_calls":[{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":` + quoteJSON(short) + `}]}`)
	if _, res := Optimize(body2, Options{Dedup: true}); res.Staled != 0 {
		t.Fatalf("短内容不应淘汰: %+v", res)
	}
}

// 重复图片折叠：同 URL 只保留最新一张
func TestDupImages(t *testing.T) {
	shot := "data:image/png;base64,AAAA_BBBB_CCCC_very_long_payload"
	mk := func() string {
		return `{"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"screenshot"},{"type":"image_url","image_url":{"url":` + quoteJSON(shot) + `}}]},` +
			`{"role":"assistant","content":"ok"},` +
			`{"role":"user","content":[{"type":"text","text":"again"},{"type":"image_url","image_url":{"url":` + quoteJSON(shot) + `}}]}]}`
	}
	out, res := Optimize([]byte(mk()), Options{Dedup: true})
	if res.ImagesDedup != 1 {
		t.Fatalf("期望折叠 1 张重复图片: %+v", res)
	}
	first := mustParse(t, out)["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)
	if first["type"] != "text" || first["text"] != imgMark {
		t.Fatalf("旧图片应替换为文字标记: %v", first)
	}
}

// 历史 reasoning 字段剥离
func TestStripReasoning(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"结论","reasoning_content":"` + strings.Repeat("思考", 300) + `"},{"role":"user","content":"hi"}]}`)
	out, res := Optimize(body, Options{Dedup: true})
	if res.Stripped != 1 || res.SavedChars <= 0 {
		t.Fatalf("期望剥离 1 条 reasoning: %+v", res)
	}
	m := mustParse(t, out)
	a := m["messages"].([]any)[0].(map[string]any)
	if _, ok := a["reasoning_content"]; ok {
		t.Fatal("reasoning_content 未剥离")
	}
	if a["content"] != "结论" {
		t.Fatal("content 被误改")
	}
}

// quoteJSON 把 Go 字符串编码为 JSON 字符串字面量（嵌入测试体用）。
func quoteJSON(s string) string {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(sb.String(), "\n")
}

func mkFileBody(lines []string, editLine string) []byte {
	content := strings.Join(lines, "\n")
	if editLine != "" {
		content += "\n" + editLine
	}
	body := map[string]any{
		"model":  "test",
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "读一下"},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{
				{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a.txt"}`}},
			}},
			{"role": "tool", "tool_call_id": "c1", "content": content},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{
				{"id": "c2", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a.txt","offset":2}`}},
			}},
			{"role": "tool", "tool_call_id": "c2", "content": content + "\n" + editLine},
		},
	}
	out, _ := json.Marshal(body)
	return out
}

func bigLines(n int) []string {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("line %d: some code that does things here", i))
	}
	return lines
}

func TestNearDupFolded(t *testing.T) {
	body := mkFileBody(bigLines(120), "the new bugfix line")
	out, res := Optimize(body, Options{Dedup: true, NearDup: true})
	if res.NearDuped != 1 {
		t.Fatalf("应折叠 1 条近重复结果, got %d", res.NearDuped)
	}
	var req map[string]any
	if json.Unmarshal(out, &req) != nil {
		t.Fatal("输出不是合法 JSON")
	}
	msgs := req["messages"].([]any)
	first := msgs[2].(map[string]any)
	if c, _ := first["content"].(string); !strings.Contains(c, "已省略近似重复") {
		t.Fatalf("旧结果应被替换为标记: %q", c)
	}
	last := msgs[4].(map[string]any)
	if c, _ := last["content"].(string); strings.Contains(c, "已省略") {
		t.Fatal("最新结果必须原样保留")
	}
}

func TestNearDupDisabled(t *testing.T) {
	body := mkFileBody(bigLines(120), "the new bugfix line")
	_, res := Optimize(body, Options{Dedup: true, NearDup: false})
	if res.NearDuped != 0 {
		t.Fatalf("NearDup=false 不应折叠, got %d", res.NearDuped)
	}
}

func TestNearDupKeepsDifferentContent(t *testing.T) {
	// 内容完全不同（<90% 相似）不应折叠
	a := bigLines(120)
	b := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		b = append(b, fmt.Sprintf("totally different content number %d with other words", i*7))
	}
	body := mkFileBody(a, strings.Join(b, "\n"))
	_, res := Optimize(body, Options{Dedup: true, NearDup: true})
	if res.NearDuped != 0 {
		t.Fatalf("不相似内容不应折叠, got %d", res.NearDuped)
	}
}

func TestNearDupChainEvolution(t *testing.T) {
	// 文件连续演变 A→B→C：A 与 C 相似度可能不足，但 A≈B、B≈C，链式折叠应处理 A 和 B
	lines := bigLines(120)
	verB := strings.Join(append(append([]string{}, lines[:118]...), "change one", "change two"), "\n")
	verC := strings.Join(append(append([]string{}, lines[:115]...), "final one", "final two", "final three", "final four", "final five"), "\n")
	ac := func(id string) map[string]any {
		return map[string]any{
			"role": "assistant", "content": "",
			"tool_calls": []map[string]any{{
				"id": id, "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": `{"path":"a.txt","offset":` + id[1:] + `}`},
			}},
		}
	}
	tc := func(id, content string) map[string]any {
		return map[string]any{"role": "tool", "tool_call_id": id, "content": content}
	}
	body := map[string]any{
		"model": "test", "stream": false,
		"messages": []map[string]any{
			ac("c1"), tc("c1", strings.Join(lines, "\n")),
			ac("c2"), tc("c2", verB),
			ac("c3"), tc("c3", verC),
		},
	}
	out, _ := json.Marshal(body)
	_, res := Optimize(out, Options{Dedup: true, NearDup: true})
	if res.NearDuped != 2 {
		t.Fatalf("链式演变应折叠 2 条旧版本, got %d", res.NearDuped)
	}
}
