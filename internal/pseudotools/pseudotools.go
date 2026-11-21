// Package pseudotools 给没有原生工具能力的模型补工具调用：
// 请求侧剥离 tools 定义、注入协议提示词、把历史里的工具往返转成文本；
// 响应侧解析模型按协议输出的 <tool_call> 标记，还原成标准 OpenAI tool_calls。
// 对客户端完全透明——它始终看到原生 tool_calls 形状。
package pseudotools

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"tokenhub/internal/provider"
)

const (
	openTag  = "<tool_call>"
	closeTag = "</tool_call>"
)

// invokeNameRe 从 <invoke name="xxx"> 里取工具名（兼容单引号）。
var invokeNameRe = regexp.MustCompile(`name\s*=\s*(?:"([^"]+)"|'([^']+)')`)

// invokeParamRe 匹配 <parameter name="key">。
var invokeParamRe = regexp.MustCompile(`<parameter\s+name\s*=\s*"([^"]+)"\s*>`)

// ── DSML：DeepSeek 网页模型自带的内部工具调用格式 ──
// <｜DSML｜calls><｜DSML｜invoke name="pwsh"><｜DSML｜parameter name="command"
// string="true">…</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜calls>
// 竖线全角｜/半角|都认；条数 1-2、内部空格、大小写均宽容（模型输出不稳定）。
// 实测还会被打散成 "</ | | DSML | | parameter>"：竖线之间、</ 之后都有空格。
// dsmlPipe 匹配 1-2 根竖线，中间可夹空格。
const dsmlPipe = `[｜|]\s*[｜|]?`

var (
	dsmlInvokeOpen  = regexp.MustCompile(`(?i)<\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*invoke[^>]*>`)
	dsmlInvokeClose = regexp.MustCompile(`(?i)</\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*invoke\s*>`)
	dsmlParamOpen   = regexp.MustCompile(`(?i)<\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*parameter[^>]*>`)
	dsmlParamClose  = regexp.MustCompile(`(?i)</\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*parameter\s*>`)
	dsmlWrapper     = regexp.MustCompile(`(?i)</?\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*calls\s*>`)
	// 兜底扫尾：以上specific匹配漏掉的任意 DSML 残渣（标签被彻底打散时）
	dsmlLoose = regexp.MustCompile(`(?i)</?\s*[｜|]{1,4}[\s|｜]*DSML[^>]{0,80}>`)
	// invoke 块之外的完整 parameter 块（开+闭都完好）：内容是内部噪音连带清掉
	dsmlParamBlock = regexp.MustCompile(`(?is)<\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*parameter[^>]*>.*?</\s*` + dsmlPipe + `\s*DSML\s*` + dsmlPipe + `\s*parameter\s*>`)

	// ── DeepSeek 会话模板特殊标记 ──
	// 「继续」续写场景高发：模型把内部对话模板当正文吐出来——
	// <｜end of sentence｜><｜User｜>继续<｜Assistant｜>…（竖线全/半角都见）。
	// 标记是纯噪音，夹着的正文要保留；DSML 标签尾部不是 ｜>，不会被误伤。
	dsmlSpecialRe = regexp.MustCompile(`<\s*[｜|][^>]{0,48}?[｜|]\s*>`)

	// ── tool_result 回声 ──
	// 模型把历史里的 <tool_result name="call_xxx">…</tool_result> 原样复述进
	// 回复（幻觉工具侧输出）。完整块连内容剥掉；未闭合时只剥标记本身。
	toolResultBlockRe = regexp.MustCompile(`(?is)<tool_result\s[^>]*>.*?</tool_result\s*>`)
	toolResultTagRe    = regexp.MustCompile(`(?i)</?tool_result[^>]*>`)
)

// wrapperTokens 部分模型（如 ERNIE）会自带包裹标记，先剥掉再解析。
var wrapperTokens = []string{
	"<ernie:tool_call>", "</ernie:tool_call>", "ernie:tool_call",
	"<function_calls>", "</function_calls>",
}

// protocolPrompt 注入的系统提示词：约定工具清单与调用标记格式。
func protocolPrompt(tools []any) string {
	var b strings.Builder
	b.WriteString("# 工具调用协议\n")
	b.WriteString("你可以调用下列工具。需要调用时，在回复中输出（可连续输出多个）：\n")
	b.WriteString("<tool_call>{\"name\":\"工具名\",\"arguments\":{\"参数\":\"值\"}}</tool_call>\n")
	b.WriteString("输出该标记后立即停止，等待系统以 <tool_result name=\"工具名\">…</tool_result> 返回结果后再继续回答。\n")
	b.WriteString("不需要工具时直接正常回答，绝不输出 <tool_call> 标记。arguments 必须是合法 JSON 对象。\n")
	b.WriteString("只允许上述 JSON 标记格式，严禁输出 <invoke>、<parameter>、<｜DSML｜invoke>、<｜DSML｜parameter> 等 XML/内部标记风格的工具调用。\n")
	b.WriteString("严禁把 <｜User｜>、<｜Assistant｜>、<｜end of sentence｜>、<tool_result> 等内部/历史标记写进回复，也不要复述历史工具结果。\n")
	b.WriteString("工具调用标记必须写在最终回复正文里，严禁放进思考/推理过程，也不要把 JSON 塞进标签属性。\n")
	b.WriteString("## 可用工具\n")
	for _, tv := range tools {
		t, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := t["function"].(map[string]any)
		if fn == nil { // 兼容旧式 {"type":"function","function":...} 之外形状
			fn = t
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		b.WriteString("### " + name + "\n")
		if desc != "" {
			b.WriteString(desc + "\n")
		}
		if params, ok := fn["parameters"]; ok && params != nil {
			if raw, err := json.Marshal(params); err == nil {
				b.WriteString("参数 Schema: " + string(raw) + "\n")
			}
		}
	}
	return b.String()
}

// flattenContent 把 OpenAI 消息 content（字符串或分段数组）压成纯文本。
func flattenContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, seg := range c {
			m, ok := seg.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["text"].(string); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// Engage 改写请求：剥离原生工具字段、注入协议提示词、工具历史转文本往返。
func Engage(body []byte) []byte {
	var src map[string]any
	if json.Unmarshal(body, &src) != nil || src == nil {
		return body
	}
	tools, _ := src["tools"].([]any)
	if len(tools) == 0 {
		return body
	}
	// 1) 剥离原生工具相关字段（上游不认会直接报错）
	for _, k := range []string{"tools", "tool_choice", "functions", "function_call", "parallel_tool_calls"} {
		delete(src, k)
	}
	// 2) 历史：assistant.tool_calls → 文本标记；role=tool → user + tool_result
	msgs, _ := src["messages"].([]any)
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "assistant":
			tcs, ok := m["tool_calls"].([]any)
			if !ok || len(tcs) == 0 {
				continue
			}
			var marks strings.Builder
			for _, tc := range tcs {
				c, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := c["function"].(map[string]any)
				if fn == nil {
					continue
				}
				name, _ := fn["name"].(string)
				args := fn["arguments"]
				if s, isStr := args.(string); isStr && s != "" {
					var obj any
					if json.Unmarshal([]byte(s), &obj) == nil && obj != nil {
						args = obj
					} else {
						args = map[string]any{}
					}
				} else if args == nil {
					args = map[string]any{}
				}
				payload, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
				marks.WriteString(openTag + string(payload) + closeTag + "\n")
			}
			if marks.Len() == 0 {
				delete(m, "tool_calls")
				continue
			}
			m["content"] = flattenContent(m["content"]) + "\n" + strings.TrimRight(marks.String(), "\n")
			delete(m, "tool_calls")
		case "tool":
			result := flattenContent(m["content"])
			name, _ := m["name"].(string)
			if name == "" {
				name, _ = m["tool_call_id"].(string)
			}
			wrapped, _ := json.Marshal([]any{map[string]any{
				"type": "text",
				"text": "<tool_result name=\"" + name + "\">" + result + "</tool_result>",
			}})
			var seg any
			_ = json.Unmarshal(wrapped, &seg) // 不能赋 []byte：会被序列化成 base64 字符串
			m["role"] = "user"
			m["content"] = seg
			delete(m, "name")
			delete(m, "tool_call_id")
		}
	}
	// 3) 注入协议提示词（插到最前面的 system 位置）
	src["messages"] = append([]any{map[string]any{"role": "system", "content": protocolPrompt(tools)}}, msgs...)
	out, err := json.Marshal(src)
	if err != nil {
		return body
	}
	return out
}

// Call 解析出的单次工具调用。
type Call struct {
	Name string
	Args string // arguments 的 JSON 字符串（OpenAI 形状）
}

// extractDSMLBlocks 剥离并解析 DeepSeek 网页模型自带的 DSML 工具调用块。
// 未闭合时也整体剥离（DSML 是纯噪音，留着只会污染回复正文）。
func extractDSMLBlocks(text string) (string, []Call) {
	if !strings.Contains(text, "DSML") && !strings.Contains(text, "dsml") {
		return text, nil
	}
	text = dsmlWrapper.ReplaceAllString(text, "")
	var b strings.Builder
	var calls []Call
	for {
		loc := dsmlInvokeOpen.FindStringIndex(text)
		if loc == nil {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:loc[0]])
		openText := text[loc[0]:loc[1]]
		rest := text[loc[1]:]
		var block string
		if cl := dsmlInvokeClose.FindStringIndex(rest); cl != nil {
			block, rest = rest[:cl[0]], rest[cl[1]:]
		} else { // 未闭合：从开标签到结尾全当块剥掉
			block, rest = rest, ""
		}
		name := ""
		if m := invokeNameRe.FindStringSubmatch(openText); m != nil {
			if m[1] != "" {
				name = m[1]
			} else {
				name = m[2]
			}
		}
	if name == "" {
		text = rest
		continue
	}
	calls = append(calls, Call{Name: name, Args: dsmlArgs(block)})
	text = rest
	}
	// 扫尾：invoke 块之外残留的 parameter 块（内容是模型内部噪音）连带清掉，
	// 再用 loose 正则清掉彻底打散的标签残渣。
	out := b.String()
	if strings.Contains(out, "DSML") || strings.Contains(out, "dsml") {
		out = dsmlParamBlock.ReplaceAllString(out, "")
		out = dsmlLoose.ReplaceAllString(out, "")
	}
	return out, calls
}

// dsmlArgs 解析 DSML invoke 块里的参数：
// <｜DSML｜parameter name="key" string="true">值</｜DSML｜parameter>。
// string="true" 表示值恒为字符串；没有参数时整块尝试按 JSON 解析。
func dsmlArgs(block string) string {
	opens := dsmlParamOpen.FindAllStringSubmatchIndex(block, -1)
	if len(opens) == 0 {
		var obj map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(block)), &obj) == nil && obj != nil {
			raw, err := json.Marshal(obj)
			if err == nil {
				return string(raw)
			}
		}
		return "{}"
	}
	params := map[string]any{}
	for idx, loc := range opens {
		openText := block[loc[0]:loc[1]]
		m := invokeNameRe.FindStringSubmatch(openText)
		if m == nil {
			continue
		}
		key := m[1]
		if key == "" {
			key = m[2]
		}
		end := len(block)
		if idx+1 < len(opens) {
			end = opens[idx+1][0]
		}
		seg := block[loc[1]:end]
		if cl := dsmlParamClose.FindStringIndex(seg); cl != nil {
			seg = seg[:cl[0]]
		}
		val := strings.TrimSpace(seg)
		// string="true" 属性声明值是字符串：即使长得像 JSON 也保持字符串
		if strings.Contains(openText, `string="true"`) || strings.Contains(openText, `string='true'`) {
			params[key] = val
		} else {
			var decoded any
			if json.Unmarshal([]byte(val), &decoded) == nil { // 数字/布尔/嵌套 JSON 保留原类型
				params[key] = decoded
			} else {
				params[key] = val
			}
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// extractInvokeBlocks 兼容模型自作主张输出的 XML 风格调用：
// <invoke name="tool"><parameter name="k">v</parameter>…</invoke>。
// 返回剥掉这些块后的正文与解析出的调用。
func extractInvokeBlocks(text string) (string, []Call) {
	if !strings.Contains(text, "<invoke") {
		return text, nil
	}
	var b strings.Builder
	var calls []Call
	for {
		i := strings.Index(text, "<invoke")
		if i < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:i])
		rest := text[i+len("<invoke"):]
		end := strings.Index(rest, "</invoke>")
		if end < 0 { // 未闭合：整体当正文保留
			b.WriteString("<invoke")
			b.WriteString(rest)
			text = ""
			break
		}
		block, tail := rest[:end], rest[end+len("</invoke>"):]
		text = tail
		m := invokeNameRe.FindStringSubmatch(block)
		name := ""
		if m != nil {
			if m[1] != "" {
				name = m[1]
			} else {
				name = m[2]
			}
		}
		if name == "" {
			continue
		}
		// 截掉开标签剩余部分（如 ` name="f">`），只留参数体
		if k := strings.Index(block, ">"); k >= 0 {
			block = block[k+1:]
		}
		calls = append(calls, Call{Name: name, Args: invokeArgs(block)})
	}
	return b.String(), calls
}

// invokeArgs 从 <invoke> 块内提取参数：优先 <parameter name="k">v</parameter> 列表，
// 没有时尝试整块按 JSON 解析。
func invokeArgs(block string) string {
	locs := invokeParamRe.FindAllStringSubmatchIndex(block, -1)
	if len(locs) == 0 {
		var obj map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(block)), &obj) == nil && obj != nil {
			raw, err := json.Marshal(obj)
			if err == nil {
				return string(raw)
			}
		}
		return "{}"
	}
	params := map[string]any{}
	for idx, loc := range locs {
		key := block[loc[2]:loc[3]]
		start := loc[1]
		end := len(block)
		if idx+1 < len(locs) {
			end = locs[idx+1][0]
		}
		seg := block[start:end]
		if j := strings.Index(seg, "</parameter>"); j >= 0 {
			seg = seg[:j]
		}
		val := strings.TrimSpace(seg)
		var decoded any
		if json.Unmarshal([]byte(val), &decoded) == nil { // 数字/布尔/嵌套 JSON 保留原类型
			params[key] = decoded
		} else {
			params[key] = val
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// reOpen/reClose 宽容匹配标记：部分模型会输出 <tool_call_xxxx> 之类带后缀的 tag。
var (
	reOpen  = regexp.MustCompile(`<tool_call[^>]*>`)
	reClose = regexp.MustCompile(`</tool_call[^>]*>`)
	reName  = regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
)

// repairJSON 把字符串内部的裸控制字符（真实换行/制表符）转成合法转义。
// 模型在 arguments 里嵌多行脚本时经常输出原始换行，标准 JSON 解析会直接失败。
func repairJSON(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	var b strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			if c == '"' {
				inStr = true
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			inStr = false
			b.WriteByte(c)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// salvageArgs JSON 整体解析失败时的兜底：从 "arguments" 开始做括号配对截取对象。
func salvageArgs(payload string) string {
	k := strings.Index(payload, `"arguments"`)
	if k < 0 {
		return "{}"
	}
	rest := payload[k+len(`"arguments"`):]
	i := strings.Index(rest, "{")
	if i < 0 {
		return "{}"
	}
	depth, inStr, esc := 0, false, false
	for j := i; j < len(rest); j++ {
		c := rest[j]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return repairJSON(rest[i : j+1])
			}
		}
	}
	return "{}"
}

// 字段边界兜底：断裂 JSON 里值内部的引号混乱会让括号配对的状态机失去同步
// （扫到结尾还停在字符串里，最后一个 } 永远配不上对）。但字段边界 ,"key":
// 仍清晰——按边界切字段、值内部引号原样保留，最后 json.Marshal 统一转义。
var (
	fieldBoundaryRe = regexp.MustCompile(`,\s*"([A-Za-z0-9_.-]+)"\s*:`)
	headFieldRe     = regexp.MustCompile(`^\s*"([A-Za-z0-9_.-]+)"\s*:`)
)

// salvageFields 从断裂的 {"arguments":{…}} 载荷里按字段边界抢救参数。
// 返回工具名（来自 "name" 字段，可能混在 arguments 里）与参数 JSON。
func salvageFields(payload string) (name, args string, ok bool) {
	k := strings.Index(payload, `"arguments"`)
	if k < 0 {
		return "", "{}", false
	}
	seg := payload[k+len(`"arguments"`):]
	if i := strings.Index(seg, "{"); i >= 0 {
		seg = seg[i+1:]
	} else {
		return "", "{}", false
	}
	if j := strings.LastIndex(seg, "}"); j >= 0 {
		seg = seg[:j]
	}
	seg = strings.TrimRight(seg, "\" \t\r\n")
	if strings.TrimSpace(seg) == "" {
		return "", "{}", false
	}
	params := map[string]any{}
	// 首个字段："key":"value…
	if head := headFieldRe.FindStringSubmatchIndex(seg); head != nil {
		end := len(seg)
		if locs := fieldBoundaryRe.FindAllStringSubmatchIndex(seg, -1); len(locs) > 0 {
			end = locs[0][0]
		}
		setField(params, &name, seg[head[2]:head[3]], extractRawValue(seg[head[1]:end]))
		// 后续字段：,"key":value…
		if locs := fieldBoundaryRe.FindAllStringSubmatchIndex(seg, -1); len(locs) > 0 {
			for idx, loc := range locs {
				e := len(seg)
				if idx+1 < len(locs) {
					e = locs[idx+1][0]
				}
				setField(params, &name, seg[loc[2]:loc[3]], extractRawValue(seg[loc[1]:e]))
			}
		}
	} else {
		return "", "{}", false
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return name, "{}", true
	}
	return name, string(raw), true
}

// setField 写入一个字段；"name" 是工具名混进了 arguments，单独存放不进参数。
func setField(params map[string]any, name *string, key string, val any) {
	if key == "name" {
		if s, isStr := val.(string); isStr {
			*name = s
			return
		}
	}
	params[key] = val
}

// extractRawValue 抠字段值：先尝试按完整 JSON 值解析（数字/布尔/合法字符串），
// 失败则按字符串处理——去掉首尾引号，值内部混乱的引号原样保留（Marshal 统一转义）。
func extractRawValue(seg string) any {
	s := strings.TrimSpace(seg)
	var v any
	if json.Unmarshal([]byte(s), &v) == nil && v != nil {
		return v
	}
	s = strings.TrimSuffix(s, "\"")
	if strings.HasPrefix(s, "\"") {
		s = s[1:]
	}
	return s
}

// parseCallPayload 解析单个标记内的 JSON 载荷，返回工具名与 arguments 字符串。
func parseCallPayload(payload string) (name, args string, ok bool) {
	var m map[string]any
	if json.Unmarshal([]byte(payload), &m) != nil {
		if json.Unmarshal([]byte(repairJSON(payload)), &m) != nil {
			// 仍失败：正则兜底抠出 name，arguments 用括号配对
			if nm := reName.FindStringSubmatch(payload); nm != nil {
				if args := salvageArgs(payload); args != "{}" {
					return nm[1], args, true
				}
				// 括号配对失效（引号混乱导致状态机失步）：按字段边界抢救
				if n, a, ok2 := salvageFields(payload); ok2 {
					if n == "" {
						n = nm[1]
					}
					return n, a, true
				}
				return nm[1], "{}", true
			}
			// name 也没抠到：字段边界兜底仍可能同时救出 name 和参数
			if n, a, ok2 := salvageFields(payload); ok2 && n != "" {
				return n, a, true
			}
			return "", "", false
		}
	}
	name, _ = m["name"].(string)
	if name == "" {
		if nm := reName.FindStringSubmatch(payload); nm != nil {
			name = nm[1]
		}
	}
	if name == "" {
		return "", "", false
	}
	switch a := m["arguments"].(type) {
	case string:
		if strings.TrimSpace(a) == "" {
			args = "{}"
		} else {
			args = a // 已是 JSON 字符串
		}
	case nil:
		args = "{}"
	default:
		raw, err := json.Marshal(a)
		if err != nil {
			args = "{}"
		} else {
			args = string(raw)
		}
	}
	return name, args, true
}

// trimToLastBrace 把候选 JSON 截到最后一个 } 为止，去掉尾巴上的标签残渣（如 `">`）。
func trimToLastBrace(s string) string {
	if j := strings.LastIndex(s, "}"); j >= 0 {
		return s[:j+1]
	}
	return s
}

// parseTagCall 解析单个 <tool_call …>…</tool_call> 标记。除约定的主体 JSON 外，
// 还兼容三种常见畸形：
// ① 整段 JSON 被塞进开标签属性：<tool_call name="{"arguments":{…},"name":"pwsh"}">
// ② 名字在属性、参数在属性或主体：<tool_call name="pwsh" arguments="{…}">
// ③ JSON 里含裸 >，开标签被 [^>]* 在 JSON 中途截断
func parseTagCall(openTagText, payload string) (Call, bool) {
	var cands []string
	addCand := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			cands = append(cands, trimToLastBrace(s))
		}
	}
	// 属性携带 JSON 的重建候选优先——payload 单独解析在截断场景会走 reName
	// 兜底「降级成功」（name 抠到但 arguments 丢失），必须让完整重建先试。
	if k := strings.Index(openTagText, "{"); k >= 0 {
		seg := openTagText[k:] // 若截断发生在 JSON 中途，被 [^>]* 吃掉的 > 就在 seg 尾部
		if j := strings.LastIndex(seg, "}"); j >= 0 {
			addCand(seg[:j+1])   // 属性携带完整 JSON
		}
		addCand(seg + payload)       // 截断拼接
		addCand(seg + ">" + payload) // 截断拼接（> 未含在 seg 尾部时补一个）
	}
	addCand(payload)
	for _, c := range cands {
		if name, args, ok := parseCallPayload(c); ok {
			return Call{Name: name, Args: args}, true
		}
	}
	// JSON 抠得出来但没带 name：名字从标签属性或全文里补
	var name string
	if m := invokeNameRe.FindStringSubmatch(openTagText); m != nil {
		name = m[1]
		if name == "" {
			name = m[2]
		}
	}
	if strings.ContainsAny(name, "{}\"'<>") { // 属性值里塞 JSON 时的误抓（name="{…）
		name = ""
		if nm := reName.FindStringSubmatch(openTagText + payload); nm != nil {
			name = nm[1]
		}
	}
	if name == "" {
		return Call{}, false
	}
	args := "{}"
	for _, c := range cands { // 任意一个候选能解出 JSON 对象就当 arguments
		var obj map[string]any
		if json.Unmarshal([]byte(repairJSON(c)), &obj) == nil && obj != nil {
			if a, has := obj["arguments"]; has { // 主体是 {arguments:{…}} 形状时取子对象
				if raw, err := json.Marshal(a); err == nil {
					args = string(raw)
				}
			} else if raw, err := json.Marshal(obj); err == nil {
				args = string(raw)
			}
			break
		}
	}
	if args == "{}" { // 最后兜底：字段边界抢救
		if _, a, ok2 := salvageFields(openTagText + payload); ok2 && a != "{}" {
			args = a
		}
	}
	return Call{Name: name, Args: args}, true
}

// stripInternalTokens 剥掉 DeepSeek 会话模板特殊标记（<｜User｜>/<｜end of
// sentence｜> 等），夹着的正文保留。没有标记形状时零拷贝原样返回。
func stripInternalTokens(text string) string {
	if !strings.ContainsAny(text, "｜") && !strings.Contains(text, "<|") {
		return text
	}
	return dsmlSpecialRe.ReplaceAllString(text, "")
}

// stripToolResultEcho 剥掉模型复述的 <tool_result> 块：完整块连内容剥掉
//（那是幻觉出来的工具侧输出，不该出现在助手回复里），未闭合的只剥标记本身。
func stripToolResultEcho(text string) string {
	if !strings.Contains(text, "tool_result") {
		return text
	}
	out := toolResultBlockRe.ReplaceAllString(text, "")
	return toolResultTagRe.ReplaceAllString(out, "")
}

// ParseText 从模型输出里抽取全部工具调用标记——兼容多种形态：
// ① 约定的 <tool_call>{JSON}</tool_call>（tag 容忍 _xxx 后缀）；
// ② 模型自带的 <invoke>/<parameter> XML 风格；
// ③ JSON 载荷含裸换行、属性携带 JSON、开标签中途截断等常见畸形，自动修复后解析。
func ParseText(text string) (content string, calls []Call) {
	for _, w := range wrapperTokens {
		text = strings.ReplaceAll(text, w, "")
	}
	// 会话模板标记与 tool_result 回声先剥（DeepSeek「继续」续写场景高发）
	text = stripInternalTokens(text)
	text = stripToolResultEcho(text)
	// DSML 优先剥离（DeepSeek 网页模型的自带格式），再走通用 XML 风格
	text, dsmlCalls := extractDSMLBlocks(text)
	text, invCalls := extractInvokeBlocks(text)
	calls = append(dsmlCalls, invCalls...)
	var b strings.Builder
	for {
		loc := reOpen.FindStringIndex(text)
		if loc == nil {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:loc[0]])
		openTagText := text[loc[0]:loc[1]]
		rest := text[loc[1]:]
		cl := reClose.FindStringIndex(rest)
		if cl == nil {
			// 未闭合标记：先尝试按调用抢救（模型截断/格式混杂时 JSON 仍可抠出；
			// 抢救成功就整体消费，避免 <tool_call> 原文漏给客户端）。
			if c, ok := parseTagCall(openTagText, rest); ok {
				calls = append(calls, c)
			} else {
				// 彻底解析失败才当正文保留，避免吞内容
				b.WriteString(openTagText)
				b.WriteString(rest)
			}
			text = ""
			break
		}
		payload := rest[:cl[0]] // 不预 TrimSpace：截断拼接时接缝处的空格要保留
		text = rest[cl[1]:]
		if c, ok := parseTagCall(openTagText, payload); ok {
			calls = append(calls, c)
		}
	}
	return strings.TrimSpace(b.String()), calls
}

// HasToolCalls 判断聚合响应（OpenAI 形状）里是否已带工具调用。
func HasToolCalls(raw []byte) bool {
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return false
	}
	return len(resp.Choices) > 0 && len(resp.Choices[0].Message.ToolCalls) > 0
}

// ApplyToResponse 聚合响应改写：把 content 里的标记还原成标准 tool_calls。
// 没有标记时原样返回（零拷贝）。
func ApplyToResponse(raw []byte) []byte {
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return raw
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return raw
	}
	ch, ok := choices[0].(map[string]any)
	if !ok {
		return raw
	}
	msg, ok := ch["message"].(map[string]any)
	if !ok {
		return raw
	}
	text, _ := msg["content"].(string)
	content, calls := ParseText(text)
	// 思考流（reasoning_content）同样可能带标记：ERNIE 等爱把 <tool_call> 吐进
	// 思考里，正文反而空着。一并解析、剥离，解析出的调用合并进 calls。
	if reason, ok := msg["reasoning_content"].(string); ok && reason != "" {
		if rContent, rCalls := ParseText(reason); rContent != reason || len(rCalls) > 0 {
			calls = append(calls, rCalls...)
			if rContent == "" {
				delete(msg, "reasoning_content") // 思考里只有标记：整段清掉
			} else {
				msg["reasoning_content"] = rContent
			}
		}
	}
	if len(calls) == 0 {
		return raw
	}
	tcs := make([]any, 0, len(calls))
	for i, c := range calls {
		tcs = append(tcs, map[string]any{
			"id":   "call_" + provider.RandomHex(12),
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": c.Args,
			},
			"index": i,
		})
	}
	msg["content"] = content
	msg["tool_calls"] = tcs
	ch["finish_reason"] = "tool_calls"
	out, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return out
}

// StripDSML 响应侧无条件噪音清理（不转 tool_calls）：客户端没声明工具时
// 伪工具协议整体不启用，模型复述上一轮调用、自发吐出的 DSML 标记、会话
// 模板特殊标记（<｜User｜> 等）、tool_result 回声都会原样漏给客户端——
// 纯噪音，直接剥掉。OpenAI/Anthropic 两种形状都处理；
// 没有噪音痕迹时零拷贝原样返回。
func StripDSML(raw []byte) (out []byte, stripped bool) {
	if !bytes.Contains(raw, []byte("DSML")) && !bytes.Contains(raw, []byte("dsml")) &&
		!bytes.Contains(raw, []byte("｜")) && !bytes.Contains(raw, []byte("<|")) &&
		!bytes.Contains(raw, []byte("tool_result")) {
		return raw, false
	}
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return raw, false
	}
	fix := func(s string) (string, bool) {
		nt, _ := extractDSMLBlocks(s)
		nt = stripInternalTokens(nt)
		nt = stripToolResultEcho(nt)
		nt = strings.TrimSpace(nt)
		return nt, nt != s
	}
	// OpenAI 形状
	if choices, _ := resp["choices"].([]any); len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if msg, ok := ch["message"].(map[string]any); ok {
				if t, ok := msg["content"].(string); ok {
					if nt, c := fix(t); c {
						msg["content"] = nt
						stripped = true
					}
				}
				if r, ok := msg["reasoning_content"].(string); ok {
					if nr, c := fix(r); c {
						msg["reasoning_content"] = nr
						stripped = true
					}
				}
			}
		}
	}
	// Anthropic 形状
	if blocks, _ := resp["content"].([]any); len(blocks) > 0 {
		for _, bv := range blocks {
			b, ok := bv.(map[string]any)
			if !ok {
				continue
			}
			switch t, _ := b["type"].(string); t {
			case "text":
				if s, ok := b["text"].(string); ok {
					if ns, c := fix(s); c {
						b["text"] = ns
						stripped = true
					}
				}
			case "thinking":
				if s, ok := b["thinking"].(string); ok {
					if ns, c := fix(s); c {
						b["thinking"] = ns
						stripped = true
					}
				}
			}
		}
	}
	if !stripped {
		return raw, false
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return raw, false
	}
	return out, true
}

// ApplyToAnthropicResponse 聚合响应改写（Anthropic 形状）：content 块里的
// 文本标记还原成 tool_use 块。没有标记时原样返回。
func ApplyToAnthropicResponse(raw []byte) []byte {
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return raw
	}
	blocks, _ := resp["content"].([]any)
	if len(blocks) == 0 {
		return raw
	}
	var text, thinking string
	for _, bv := range blocks {
		b, ok := bv.(map[string]any)
		if !ok {
			continue
		}
		switch t, _ := b["type"].(string); t {
		case "text":
			text += b["text"].(string) + "\n"
		case "thinking": // 思考块同样可能带标记，一并解析
			th, _ := b["thinking"].(string)
			thinking += th + "\n"
		}
	}
	content, calls := ParseText(text)
	thinkContent, thinkCalls := ParseText(thinking)
	calls = append(calls, thinkCalls...)
	if len(calls) == 0 {
		return raw
	}
	out := []any{}
	if thinkContent != "" {
		out = append(out, map[string]any{"type": "thinking", "thinking": thinkContent})
	}
	if content != "" {
		out = append(out, map[string]any{"type": "text", "text": content})
	}
	for _, c := range calls {
		input := map[string]any{}
		if json.Unmarshal([]byte(c.Args), &input) != nil {
			input = map[string]any{}
		}
		out = append(out, map[string]any{
			"type": "tool_use",
			"id":   "toolu_" + provider.RandomHex(12),
			"name": c.Name,
			"input": input,
		})
	}
	resp["content"] = out
	resp["stop_reason"] = "tool_use"
	rawOut, err := json.Marshal(resp)
	if err != nil {
		return raw
	}
	return rawOut
}
