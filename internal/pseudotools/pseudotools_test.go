package pseudotools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestEngageStripsToolsAndInjectsPrompt(t *testing.T) {
	body := []byte(`{
		"model":"m","messages":[
			{"role":"system","content":"你是助手"},
			{"role":"user","content":"北京天气"},
			{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},
			{"role":"tool","tool_call_id":"c1","name":"get_weather","content":"晴 25度"},
			{"role":"user","content":"那上海呢"}
		],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"查天气","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],
		"tool_choice":"auto"
	}`)
	out := Engage(body)
	var m struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("engage 输出不是合法 JSON")
	}
	if m.Tools != nil {
		t.Error("tools 未被剥离")
	}
	if len(m.Messages) != 6 {
		t.Fatalf("messages 数量=%d, want 6 (原 5 条 + 注入 system)", len(m.Messages))
	}
	var sys string
	json.Unmarshal(m.Messages[0].Content, &sys)
	if !strings.Contains(sys, "get_weather") || !strings.Contains(sys, "<tool_call>") {
		t.Error("协议提示词未注入工具清单")
	}
	// assistant 历史：tool_calls → 文本标记
	var asst string
	json.Unmarshal(m.Messages[3].Content, &asst)
	if !strings.Contains(asst, "<tool_call>") || m.Messages[3].ToolCalls != nil {
		t.Errorf("assistant 历史未转换: %s", asst)
	}
	// tool 历史 → user + tool_result（content 为分段数组）
	var segs []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Messages[4].Content, &segs) != nil {
		t.Fatalf("tool 历史不是分段数组: %s", m.Messages[4].Content)
	}
	var toolText string
	for _, s := range segs {
		toolText += s.Text
	}
	if !strings.Contains(toolText, "<tool_result name=\"get_weather\">") || !strings.Contains(toolText, "晴 25度") {
		t.Errorf("tool 历史未转换: %s", toolText)
	}
	if m.Messages[4].Role != "user" {
		t.Errorf("tool→user 角色未改: %q", m.Messages[4].Role)
	}
}

func TestParseText(t *testing.T) {
	text := "好的，我来查一下。\n<tool_call>{\"name\":\"get_weather\",\"arguments\":{\"city\":\"北京\"}}</tool_call>\n<tool_call>{\"name\":\"get_time\",\"arguments\":{\"tz\":\"+8\"}}</tool_call>"
	content, calls := ParseText(text)
	if content != "好的，我来查一下。" {
		t.Errorf("content=%q", content)
	}
	if len(calls) != 2 || calls[0].Name != "get_weather" || calls[1].Name != "get_time" {
		t.Fatalf("calls=%+v", calls)
	}
	var args map[string]any
	if json.Unmarshal([]byte(calls[0].Args), &args) != nil || args["city"] != "北京" {
		t.Errorf("args=%q", calls[0].Args)
	}
	// 字符串形式的 arguments 也接受
	_, calls2 := ParseText("<tool_call>{\"name\":\"f\",\"arguments\":\"{\\\"a\\\":1}\"}</tool_call>")
	if len(calls2) != 1 || calls2[0].Args != "{\"a\":1}" {
		t.Errorf("string args 解析失败: %+v", calls2)
	}
	// 未闭合标记不吞正文
	content3, calls3 := ParseText("前文<tool_call>{\"name\":\"f\"}")
	if content3 == "" || calls3 != nil {
		t.Errorf("未闭合处理异常: content=%q calls=%+v", content3, calls3)
	}
}

// ERNIE 等模型不按约定格式来，输出 XML 风格 <invoke>/<parameter>（线上实测样本）。
func TestParseTextInvokeFormat(t *testing.T) {
	text := "<ernie:tool_call> <invoke name=\"pwsh\"> <parameter name=\"command\">& python.exe -c \"print(1)\"</parameter> <parameter name=\"description\">Inspect libgojni.so</parameter> </invoke> <invoke name=\"pwsh\"> <parameter name=\"command\">dir C:\\Temp</parameter> <parameter name=\"description\">List temp</parameter> </invoke> </ernie:tool_call>"
	content, calls := ParseText(text)
	if strings.TrimSpace(content) != "" {
		t.Errorf("正文未清理: %q", content)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%+v", calls)
	}
	var a1 map[string]any
	if json.Unmarshal([]byte(calls[0].Args), &a1) != nil {
		t.Fatalf("args 不合法: %q", calls[0].Args)
	}
	if a1["command"] != "& python.exe -c \"print(1)\"" || a1["description"] != "Inspect libgojni.so" {
		t.Errorf("args 解析错误: %v", a1)
	}
	if calls[1].Name != "pwsh" {
		t.Errorf("第二个调用名=%q", calls[1].Name)
	}
	// 混合正文 + invoke 块
	content2, calls2 := ParseText("我先看看文件。<invoke name=\"read_file\"><parameter name=\"path\">a.txt</parameter></invoke>")
	if content2 != "我先看看文件。" || len(calls2) != 1 || calls2[0].Name != "read_file" {
		t.Errorf("content=%q calls=%+v", content2, calls2)
	}
	// 整块 JSON 的 invoke 变体
	_, calls3 := ParseText("<invoke name=\"f\">{\"a\":1}</invoke>")
	if len(calls3) != 1 || calls3[0].Args != "{\"a\":1}" {
		t.Errorf("JSON invoke 解析失败: %+v", calls3)
	}
}

// 模型会输出 <tool_call_xxx> 带后缀的 tag，且 arguments 里嵌带真实换行的脚本。
func TestParseTextTolerantTagAndRawNewlines(t *testing.T) {
	text := "<tool_call_4d>{\"name\":\"pwsh\",\"arguments\":{\"command\":\"import re\nimport os\nprint(1)\"}}</tool_call>"
	content, calls := ParseText(text)
	if strings.TrimSpace(content) != "" {
		t.Errorf("正文未清理: %q", content)
	}
	if len(calls) != 1 || calls[0].Name != "pwsh" {
		t.Fatalf("calls=%+v", calls)
	}
	if !strings.Contains(calls[0].Args, "import re\\nimport os") {
		t.Errorf("裸换行未转义: %q", calls[0].Args)
	}
	// JSON 彻底损坏时正则兜底抠 name + arguments 括号配对
	text2 := "<tool_call>{\"name\":\"pwsh\",\"arguments\":{\"command\":\"a\\\"b{c}d\"}} 残缺</tool_call>"
	_, calls2 := ParseText(text2)
	if len(calls2) != 1 || calls2[0].Name != "pwsh" {
		t.Fatalf("calls2=%+v", calls2)
	}
	if calls2[0].Args != `{"command":"a\"b{c}d"}` {
		t.Errorf("arguments 配对错误: %q", calls2[0].Args)
	}
	// 无 name 的载荷跳过，不产生调用
	_, calls3 := ParseText("<tool_call>{\"arguments\":{\"command\":\"x\"}}</tool_call>")
	if calls3 != nil {
		t.Errorf("缺 name 不应产生调用: %+v", calls3)
	}
}

// 线上实测（2026-11-15）：ERNIE 5.5 把整段 JSON 塞进开标签属性，或把标记吐进思考流。
func TestParseTextAttributeJSON(t *testing.T) {
	// ① 整段 JSON 塞进 name 属性，主体为空
	text := "<tool_call name=\"{\"arguments\":{\"command\":\"ls -la\",\"timeoutMs\":300000},\"name\":\"pwsh\"}\"></tool_call>"
	content, calls := ParseText(text)
	if strings.TrimSpace(content) != "" {
		t.Errorf("正文未清理: %q", content)
	}
	if len(calls) != 1 || calls[0].Name != "pwsh" {
		t.Fatalf("calls=%+v", calls)
	}
	var a map[string]any
	if json.Unmarshal([]byte(calls[0].Args), &a) != nil || a["command"] != "ls -la" {
		t.Errorf("args=%q", calls[0].Args)
	}

	// ② JSON 里带裸 >，开标签被 [^>]* 在 JSON 中途截断
	text2 := "<tool_call name=\"{\"arguments\":{\"command\":\"a > b | Out-String\"},\"name\":\"pwsh\"}\"></tool_call>"
	_, calls2 := ParseText(text2)
	if len(calls2) != 1 || calls2[0].Name != "pwsh" {
		t.Fatalf("calls2=%+v", calls2)
	}
	var a2 map[string]any
	if json.Unmarshal([]byte(calls2[0].Args), &a2) != nil || a2["command"] != "a > b | Out-String" {
		t.Errorf("args2=%q", calls2[0].Args)
	}

	// ③ name 在属性、arguments 在属性的拆分形态
	_, calls3 := ParseText("<tool_call name=\"pwsh\" arguments=\"{\"command\":\"dir\"}\"></tool_call>")
	if len(calls3) != 1 || calls3[0].Name != "pwsh" {
		t.Fatalf("calls3=%+v", calls3)
	}
	var a3 map[string]any
	if json.Unmarshal([]byte(calls3[0].Args), &a3) != nil || a3["command"] != "dir" {
		t.Errorf("args3=%q", calls3[0].Args)
	}

	// ④ name 在属性、JSON 主体缺 name
	_, calls4 := ParseText("<tool_call name=\"pwsh\">{\"arguments\":{\"command\":\"echo hi\"}}</tool_call>")
	if len(calls4) != 1 || calls4[0].Name != "pwsh" {
		t.Fatalf("calls4=%+v", calls4)
	}
	var a4 map[string]any
	if json.Unmarshal([]byte(calls4[0].Args), &a4) != nil || a4["command"] != "echo hi" {
		t.Errorf("args4=%q", calls4[0].Args)
	}
}

// 思考流（reasoning_content）里的标记也要解析、剥离（ERNIE 惯犯行为）。
func TestApplyToResponseReasoning(t *testing.T) {
	mk := func(reasoning, content string) []byte {
		return []byte(`{"choices":[{"message":{"role":"assistant","content":` + strconv.Quote(content) + `,"reasoning_content":` + strconv.Quote(reasoning) + `},"finish_reason":"stop"}]}`)
	}
	raw := mk("先看看<tool_call>{\"name\":\"pwsh\",\"arguments\":{\"command\":\"ls\"}}</tool_call>", "")
	out := ApplyToResponse(raw)
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(out, &resp) != nil {
		t.Fatal("输出不合法")
	}
	c := resp.Choices[0]
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].Function.Name != "pwsh" {
		t.Fatalf("tool_calls=%+v", c.Message.ToolCalls)
	}
	if c.Message.ReasoningContent != "先看看" {
		t.Errorf("思考流清理异常: %q", c.Message.ReasoningContent)
	}
	if c.FinishReason != "tool_calls" {
		t.Errorf("finish_reason=%q", c.FinishReason)
	}
	// 思考流有正常文本 + 标记：文本保留、标记剥离
	out2 := ApplyToResponse(mk("计划：<tool_call>{\"name\":\"f\",\"arguments\":{}}</tool_call>", "正文"))
	var resp2 struct {
		Choices []struct {
			Message struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(out2, &resp2)
	if resp2.Choices[0].Message.ReasoningContent != "计划：" {
		t.Errorf("思考正文被误删: %q", resp2.Choices[0].Message.ReasoningContent)
	}
	// 无标记时思考流原样保留
	raw3 := mk("纯思考", "正文")
	if out3 := ApplyToResponse(raw3); string(out3) != string(raw3) {
		t.Errorf("无标记时不应改写: %s", out3)
	}
}

func TestApplyToResponse(t *testing.T) {
	raw := []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"<tool_call>{\"name\":\"get_weather\",\"arguments\":{\"city\":\"北京\"}}</tool_call>"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	out := ApplyToResponse(raw)
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(out, &resp) != nil {
		t.Fatal("输出不合法")
	}
	c := resp.Choices[0]
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls=%+v", c.Message.ToolCalls)
	}
	if c.Message.Content != "" {
		t.Errorf("content 未清理: %q", c.Message.Content)
	}
	if c.FinishReason != "tool_calls" {
		t.Errorf("finish_reason=%q", c.FinishReason)
	}
	// 无标记原样返回
	if out2 := ApplyToResponse([]byte(`{"choices":[{"message":{"content":"hi"}}]}`)); string(out2) != `{"choices":[{"message":{"content":"hi"}}]}` {
		t.Errorf("无标记时不应改写: %s", out2)
	}
}

func TestApplyToAnthropicResponse(t *testing.T) {
	raw := []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"查询中<tool_call>{\"name\":\"get_weather\",\"arguments\":{\"city\":\"上海\"}}</tool_call>"}],"stop_reason":"end_turn"}`)
	out := ApplyToAnthropicResponse(raw)
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if json.Unmarshal(out, &resp) != nil {
		t.Fatal("输出不合法")
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason=%q", resp.StopReason)
	}
	var toolCount, textCount int
	for _, b := range resp.Content {
		if b.Type == "tool_use" && b.Name == "get_weather" {
			toolCount++
		}
		if b.Type == "text" && b.Text == "查询中" {
			textCount++
		}
	}
	if toolCount != 1 || textCount != 1 {
		t.Errorf("content 块异常: %+v", resp.Content)
	}
}
