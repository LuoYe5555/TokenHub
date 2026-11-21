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
	// 未闭合标记：JSON 可抢救时消费为调用，前置正文保留（2026-11-21 线上样本）
	content3, calls3 := ParseText("前文<tool_call>{\"name\":\"f\"}")
	if content3 != "前文" || len(calls3) != 1 || calls3[0].Name != "f" {
		t.Errorf("未闭合抢救异常: content=%q calls=%+v", content3, calls3)
	}
	// 彻底无法解析（无 name）时才保留正文
	content4, calls4 := ParseText("前文<tool_call>纯文本没有JSON")
	if content4 == "" || calls4 != nil {
		t.Errorf("未闭合无JSON应保留正文: content=%q calls=%+v", content4, calls4)
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

// DeepSeek 网页模型自带 DSML 内部格式（2026-11-21 线上实测样本）：
// <｜DSML｜calls><｜DSML｜invoke name="pwsh"><｜DSML｜parameter name="command"
// string="true">…</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜calls>
func TestParseTextDSML(t *testing.T) {
	text := "<｜DSML｜calls> <｜DSML｜invoke name=\"pwsh\"> <｜DSML｜parameter name=\"command\" string=\"true\">d = \"C:\\Users\\test\"; Get-ChildItem d -Force</｜DSML｜parameter> <｜DSML｜parameter name=\"description\" string=\"true\">检查目标目录</｜DSML｜parameter> </｜DSML｜invoke> </｜DSML｜calls>"
	content, calls := ParseText(text)
	if strings.TrimSpace(content) != "" {
		t.Errorf("正文未清理: %q", content)
	}
	if len(calls) != 1 || calls[0].Name != "pwsh" {
		t.Fatalf("calls=%+v", calls)
	}
	var a map[string]any
	if json.Unmarshal([]byte(calls[0].Args), &a) != nil {
		t.Fatalf("args 不合法: %q", calls[0].Args)
	}
	if a["command"] != "d = \"C:\\Users\\test\"; Get-ChildItem d -Force" {
		t.Errorf("command=%q", a["command"])
	}
	if a["description"] != "检查目标目录" {
		t.Errorf("description=%q", a["description"])
	}

	// 半角竖线 |DSML| 变体 + 多调用 + 前置正文
	text2 := "先查一下。<|DSML|calls><|DSML|invoke name=\"read_file\"><|DSML|parameter name=\"path\">a.txt</|DSML|parameter></|DSML|invoke><|DSML|invoke name=\"pwsh\"><|DSML|parameter name=\"command\" string=\"true\">dir</|DSML|parameter></|DSML|invoke></|DSML|calls>"
	content2, calls2 := ParseText(text2)
	if content2 != "先查一下。" {
		t.Errorf("content2=%q", content2)
	}
	if len(calls2) != 2 || calls2[0].Name != "read_file" || calls2[1].Name != "pwsh" {
		t.Fatalf("calls2=%+v", calls2)
	}
	var a2 map[string]any
	json.Unmarshal([]byte(calls2[0].Args), &a2)
	if a2["path"] != "a.txt" {
		t.Errorf("path=%q", a2["path"])
	}

	// 未闭合块：整体剥离不残留 DSML
	text3 := "<｜DSML｜invoke name=\"pwsh\"><｜DSML｜parameter name=\"command\" string=\"true\">echo hi"
	content3, calls3 := ParseText(text3)
	if strings.Contains(content3, "DSML") {
		t.Errorf("未闭合 DSML 残留: %q", content3)
	}
	if len(calls3) != 1 || calls3[0].Name != "pwsh" {
		t.Fatalf("calls3=%+v", calls3)
	}
	var a3 map[string]any
	json.Unmarshal([]byte(calls3[0].Args), &a3)
	if a3["command"] != "echo hi" {
		t.Errorf("command3=%q", a3["command"])
	}
}

// 无 tools 请求（伪工具协议未启用）也会漏 DSML：模型复述上一轮调用。
// StripDSML 必须无条件剥离、且不产生 tool_calls。
func TestStripDSML(t *testing.T) {
	raw := []byte(`{"choices":[{"message":{"role":"assistant","content":"环境确认完毕。\n<｜DSML｜calls><｜DSML｜invoke name=\"pwsh\"><｜DSML｜parameter name=\"command\" string=\"true\">Get-ChildItem $d -Force</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜calls>"},"finish_reason":"stop"}]}`)
	out, stripped := StripDSML(raw)
	if !stripped {
		t.Fatal("未检测到 DSML 剥离")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []any  `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(out, &resp) != nil {
		t.Fatal("输出不合法")
	}
	c := resp.Choices[0].Message
	if c.Content != "环境确认完毕。" {
		t.Errorf("content=%q", c.Content)
	}
	if len(c.ToolCalls) != 0 {
		t.Errorf("无 tools 请求不应产生 tool_calls: %+v", c.ToolCalls)
	}
	// 无 DSML 时零拷贝
	if out2, s2 := StripDSML([]byte(`{"choices":[{"message":{"content":"正常"}}]}`)); s2 || string(out2) != `{"choices":[{"message":{"content":"正常"}}]}` {
		t.Errorf("无 DSML 不应改写: %s", out2)
	}
	// 变体：双竖线、带空格、小写
	raw2 := []byte(`{"choices":[{"message":{"content":"<|| DSML || invoke name=\"f\"><|| DSML || parameter name=\"a\">v</|| DSML || parameter></|| DSML || invoke>尾声"}}]}`)
	out2, s2 := StripDSML(raw2)
	if !s2 {
		t.Fatal("变体未剥离")
	}
	var resp2 struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(out2, &resp2)
	if resp2.Choices[0].Message.Content != "尾声" {
		t.Errorf("变体 content=%q", resp2.Choices[0].Message.Content)
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

// 2026-11-21 线上实测：DSML 标签被打散（竖线间带空格、</ 后带空格），且
// <tool_call> 未闭合、JSON 中途断掉混入 DSML 残片——两个变体叠加泄漏。
func TestParseTextDSMLSpacedAndHybrid(t *testing.T) {
	// ① 打散的 DSML 标签：specific 正则 + loose 扫尾都要能清
	text := "开头 </ | | DSML | | parameter> < | | DSML | | parameter name=\"description\" string=\"true\">验证直连与 GPT 分流行为</ | | DSML | | parameter> </ | | DSML | | invoke>"
	content, calls := ParseText(text)
	if strings.Contains(content, "DSML") || strings.Contains(content, "|") {
		t.Errorf("打散 DSML 残留: %q", content)
	}
	if len(calls) != 0 {
		t.Errorf("纯残渣不应产生调用: %+v", calls)
	}

	// ② 完整混合场景（截图还原）：未闭合 <tool_call> + 断裂 JSON（裸换行、
	// PowerShell 花括号）+ 打散 DSML 残片 → 抢救成一个调用，正文零残留
	text2 := "<tool_call>{\"arguments\":{\"command\":\"cd \\\"C:\\\\Users\\\\30955\\\\Desktop\\\\test\\\"; \"===[step]===\"; try{r=1}catch{ \"err \" + 1 };\"done\",\"description\":\"验证\",\"name\":\"pwsh\"} </ | | DSML | | parameter> < | | DSML | | parameter name=\"description\" string=\"true\">验证直连</ | | DSML | | parameter> </ | | DSML | | invoke>"
	content2, calls2 := ParseText(text2)
	if strings.Contains(content2, "DSML") || strings.Contains(content2, "tool_call") {
		t.Errorf("混合场景正文残留: %q", content2)
	}
	if len(calls2) != 1 || calls2[0].Name != "pwsh" {
		t.Fatalf("混合场景 calls=%+v", calls2)
	}
	var a map[string]any
	if json.Unmarshal([]byte(calls2[0].Args), &a) != nil {
		t.Fatalf("args 不合法: %q", calls2[0].Args)
	}
	if cmd, _ := a["command"].(string); !strings.Contains(cmd, "Desktop") {
		t.Errorf("command 未抢救出: %q", calls2[0].Args)
	}

	// ③ StripDSML 对打散变体也生效（无 tools 轮次的复述场景）
	raw := []byte(`{"choices":[{"message":{"content":"结论 </ | | DSML | | parameter> </ | | DSML | | invoke>"}}]}`)
	out, stripped := StripDSML(raw)
	if !stripped {
		t.Fatal("打散变体未触发剥离")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(out, &resp)
	if strings.Contains(resp.Choices[0].Message.Content, "DSML") {
		t.Errorf("StripDSML 后残留: %q", resp.Choices[0].Message.Content)
	}
}

// 2026-11-21 晚线上实测：「继续」续写场景模型把内部会话模板当正文吐出来
//（<｜end of sentence｜><｜User｜>继续…），且把历史 tool_result 原样复述。
func TestStripInternalTokensAndToolResult(t *testing.T) {
	// ① 模板标记剥掉、夹着的正文保留
	text := "<｜end of sentence｜><｜User｜>继续<｜User｜>问题找到了：管道混流了。<｜Assistant｜>先停止旧进程再重跑。"
	content, calls := ParseText(text)
	if strings.Contains(content, "｜") || strings.Contains(content, "end of sentence") {
		t.Errorf("模板标记残留: %q", content)
	}
	if !strings.Contains(content, "继续") || !strings.Contains(content, "问题找到了") {
		t.Errorf("正文被误删: %q", content)
	}
	if len(calls) != 0 {
		t.Errorf("不应产生调用: %+v", calls)
	}

	// ② 半角竖线变体 + tool_result 完整块连内容剥掉
	text2 := "先看结果。<|end of sentence|><tool_result name=\"call_abc123\">=== 规则匹配测试 ===\\n[FAIL] check</tool_result>结论如下。"
	content2, _ := ParseText(text2)
	if strings.Contains(content2, "tool_result") || strings.Contains(content2, "FAIL") || strings.Contains(content2, "end of sentence") {
		t.Errorf("回声/标记残留: %q", content2)
	}
	if !strings.Contains(content2, "结论如下") || !strings.Contains(content2, "先看结果") {
		t.Errorf("正文被误删: %q", content2)
	}

	// ③ 未闭合 tool_result：标记剥掉
	content3, _ := ParseText("开头 <tool_result name=\"call_x\">未闭合内容")
	if strings.Contains(content3, "tool_result") {
		t.Errorf("未闭合标记残留: %q", content3)
	}

	// ④ 无 tools 轮次：StripDSML 一并处理模板标记与回声
	raw := []byte(`{"choices":[{"message":{"content":"<｜end of sentence｜><｜User｜>继续<tool_result name=\"c1\">noise</tool_result>好了"}}]}`)
	out, stripped := StripDSML(raw)
	if !stripped {
		t.Fatal("未触发剥离")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(out, &resp)
	c := resp.Choices[0].Message.Content
	if strings.Contains(c, "｜") || strings.Contains(c, "tool_result") || strings.Contains(c, "noise") {
		t.Errorf("StripDSML 清理不彻底: %q", c)
	}
	if !strings.Contains(c, "好了") {
		t.Errorf("正文被误删: %q", c)
	}

	// ⑤ 零拷贝：无任何噪音痕迹
	if out2, s2 := StripDSML([]byte(`{"choices":[{"message":{"content":"正常中文"}}]}`)); s2 || string(out2) != `{"choices":[{"message":{"content":"正常中文"}}]}` {
		t.Errorf("无噪音不应改写: %s", out2)
	}
}
