// Package tokensaver 网关层 token 优化：对请求消息做确定性、离线的
// 无损压缩，降低发往上游的输入 token，不改变语义（不删消息、不改顺序、
// 不丢关键信息、不换模型）。参考 sqz / headroom 的思路并取其"绝对安全"子集：
//
//  1. JSON 紧凑化 —— 工具结果 / tool_call 参数里 pretty-print 的 JSON
//     用 json.Compact 压平。只删除 JSON 语法空白，字符串值逐字节保留。
//
//  2. 会话内重复折叠 —— 同一次请求里 byte 级完全相同的大段内容
//     （典型：agent 反复读同一份未改动的文件）只保留最新一份，
//     更早的替换为一句短标记。
//
//  3. 过期工具结果淘汰 —— 同一工具以相同参数被多次调用时（典型：
//     agent 改几行代码就重读整个文件），旧结果已被新调用取代，
//     只保留最新一次，旧的换成短标记。这是 agent 长会话里最大头
//     的浪费：每次重读哪怕文件只改了一行，整份新内容都会入上下文，
//     而旧版本之后每一轮都在白付 token。
//
//  4. 重复图片折叠 —— 内容完全相同的图片（截图反复回传）只保留
//     最新一张，旧的换成文字标记。图片是 token 大户（每张数千）。
//
//  5. 剥离历史 reasoning 字段 —— reasoning_content / reasoning 是
//     模型的思考过程回显，上游请求从不需要（各 provider 均只在响应
//     里输出、不在请求里读取），客户端误回传时纯属浪费。
//
//  6. 近重复工具结果折叠 —— 同一工具前后多次调用的结果高度相似时
//     （典型：agent 改了一行后重读文件、或换 offset/limit 读同一份
//     大文件——参数变了导致第 3 条的"同参淘汰"漏网），只保留最新
//     一份，旧的换成短标记。按"行多重集相似度 ≥ 90%"判定，链式比对
//     （每个旧版本跟它后面最近保留的版本比），支持文件连续演变。
//
// 明确不做的（会改变效果，故不做）：语义缓存、截断/摘要工具输出、
// 空白行规整（编码 agent 依赖原文格式做补丁）、模型路由。
package tokensaver

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Options 优化开关。MinChars 为重复折叠的最小内容长度（字符）。
type Options struct {
	Dedup       bool // 折叠会话内重复内容
	CompactJSON bool // JSON 紧凑化
	NearDup     bool // 近重复工具结果折叠（同一工具、内容高度相似的旧结果）
	MinChars    int  // 默认 500
}

// Result 一次优化的统计。
type Result struct {
	SavedChars  int64 // 减少的字符数
	SavedTokens int64 // 按 EstTokens 口径估算的节省 token
	Deduped     int   // 折叠的重复消息条数
	Compacted   int   // 紧凑化的 JSON 条数
	Staled      int   // 淘汰的过期工具结果条数
	ImagesDedup int   // 折叠的重复图片张数
	Stripped    int   // 剥离的 reasoning 字段条数
	NearDuped   int   // 折叠的近重复工具结果条数
}

// dedupMark 替换重复内容用的短标记（模型可读：告知完整内容仍在上下文）。
const dedupMark = "[TokenHub 已折叠重复内容：本条消息与本次对话上下文中另一条消息的内容完全相同，完整内容仍保留在上下文中，无需重复阅读]"

// staleMark 替换过期工具结果的短标记。
const staleMark = "[TokenHub 已省略过期结果：同一工具以相同参数再次调用过，最新结果见下方对应条目，此处旧结果已无参考价值]"

// nearDupMark 替换近重复工具结果的短标记。
const nearDupMark = "[TokenHub 已省略近似重复的旧结果：本结果与上下文中最新一次同名工具调用的结果高度相似（通常为同一内容的旧版本或不同参数的重复读取），差异请以最新结果为准]"

// imgMark 替换重复图片的文字标记。
const imgMark = "[TokenHub 已省略重复图片：与上下文中最新一张完全相同，图片内容保留在最新一处]"

// staleMinChars 过期工具结果淘汰的最小内容长度（字符）。
const staleMinChars = 300

// nearDupMinChars 近重复折叠的最小内容长度（字符）。短结果折叠收益太小，
// 且误折叠风险相对更高。
const nearDupMinChars = 1500

// nearDupThreshold 行多重集相似度阈值：公共行占比 ≥ 此值判为近重复。
const nearDupThreshold = 0.9

// nearDupMaxLines 相似度比对时每条文本最多取的行数（防超大文件拖慢请求）。
const nearDupMaxLines = 20000

// EstTokens 项目通用的 token 粗估口径（与 count_tokens 一致）：
// 中英混合按 runes/2 + bytes/6。
func EstTokens(s string) int64 {
	return int64(utf8.RuneCountInString(s))/2 + int64(len(s))/6
}

// Optimize 解析 OpenAI 格式请求体并优化 messages，返回（可能不变的）新请求体。
// 解析失败时原样返回，绝不因优化引入请求错误。
func Optimize(body []byte, opt Options) ([]byte, Result) {
	if opt.MinChars <= 0 {
		opt.MinChars = 500
	}
	if !opt.Dedup && !opt.CompactJSON {
		return body, Result{}
	}
	// UseNumber 保证数字字段逐字节往返（大整数 ID 不被 float64 破坏）
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var req map[string]any
	if err := dec.Decode(&req); err != nil {
		return body, Result{}
	}
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, Result{}
	}

	var res Result

	// ── Pass 1: JSON 紧凑化 ──
	if opt.CompactJSON {
		for _, mv := range msgs {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			// tool_calls[].function.arguments
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, tv := range tcs {
					tc, ok := tv.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tc["function"].(map[string]any)
					if fn == nil {
						continue
					}
					args, ok := fn["arguments"].(string)
					if !ok || !needsCompact(args) {
						continue
					}
					if c, ok2 := compactJSON(args); ok2 && len(c) < len(args) {
						fn["arguments"] = c
						res.Compacted++
						res.SavedChars += int64(len(args) - len(c))
					}
				}
			}
			// 消息 content：整段是 JSON 时压平
			if txt, idx, ok := contentText(m); ok && needsCompact(txt) {
				if c, ok2 := compactJSON(txt); ok2 && len(c) < len(txt) {
					setContent(m, idx, c)
					res.Compacted++
					res.SavedChars += int64(len(txt) - len(c))
				}
			}
		}
	}

	// ── Pass 2: 会话内重复折叠（在紧凑化之后按最终文本比对） ──
	if opt.Dedup {
		type hit struct {
			m    map[string]any
			idx  int // content 为数组时的 text 块下标；-1=字符串
			text string
		}
		seen := map[string][]hit{}
		for _, mv := range msgs {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			if role != "tool" && role != "user" && role != "assistant" {
				continue // 不动 system（保持前缀稳定）
			}
			txt, idx, ok := contentText(m)
			if !ok || utf8.RuneCountInString(txt) < opt.MinChars {
				continue
			}
			sum := sha256.Sum256([]byte(txt))
			seen[string(sum[:])] = append(seen[string(sum[:])], hit{m: m, idx: idx, text: txt})
		}
		for _, list := range seen {
			if len(list) < 2 {
				continue
			}
			// 保留最新一份（列表末位），其余折叠
			for _, h := range list[:len(list)-1] {
				setContent(h.m, h.idx, dedupMark)
				res.Deduped++
				res.SavedChars += int64(len(h.text) - len(dedupMark))
			}
		}
	}

	// ── Pass 3: 过期工具结果淘汰（同工具同参的旧调用结果） ──
	if opt.Dedup {
		foldStaleToolResults(msgs, &res)
		// ── Pass 6: 近重复工具结果折叠（同工具、内容高度相似的旧结果，
		//    覆盖"参数变了但内容基本没变"的漏网场景） ──
		if opt.NearDup {
			foldNearDupToolResults(msgs, &res)
		}
		// ── Pass 4: 重复图片折叠 ──
		foldDupImages(msgs, &res)
		// ── Pass 5: 剥离历史 reasoning 回显字段 ──
		stripReasoning(msgs, &res)
	}

	if res.SavedChars <= 0 {
		return body, Result{}
	}
	out := marshalCompact(req)
	res.SavedTokens = EstTokens(string(body)) - EstTokens(string(out))
	if res.SavedTokens < 0 {
		res.SavedTokens = 0
	}
	return out, res
}

// normArgsKey 把工具参数规范化为稳定 key：合法 JSON 时解析后重序列化
//（map 键自动排序，键序/空白差异归一）；非法 JSON 退回压缩形式。
func normArgsKey(args string) string {
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err == nil {
		if out := marshalCompact(v); len(out) > 0 {
			return string(out)
		}
	}
	if c, ok := compactJSON(args); ok {
		return c
	}
	return args
}

// foldStaleToolResults 过期工具结果淘汰：同一 (工具名+参数) 被多次调用时，
// 只保留最新一次的结果，更早的替换为短标记。参数做 JSON 规范化比对
// （键序/空白差异视为相同调用）。
func foldStaleToolResults(msgs []any, res *Result) {
	// 1) tool_call_id → 规范化 key（来自 assistant 消息的 tool_calls）
	idKey := map[string]string{}
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := m["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tv := range tcs {
			tc, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			if id == "" || fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}
			args, _ := fn["arguments"].(string)
			args = normArgsKey(args)
			idKey[id] = name + "\x00" + args
		}
	}
	if len(idKey) == 0 {
		return
	}
	// 2) 顺序收集 tool 消息（key 齐全且内容够长），记录最后出现位置
	type occ struct {
		m    map[string]any
		text string
	}
	lastIdx := map[string]int{}
	var order []struct {
		key string
		occ occ
	}
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "tool" {
			continue
		}
		id, _ := m["tool_call_id"].(string)
		key, ok := idKey[id]
		if !ok {
			continue
		}
		txt, _, ok := contentText(m)
		if !ok || utf8.RuneCountInString(txt) < staleMinChars || txt == staleMark {
			continue
		}
		order = append(order, struct {
			key string
			occ occ
		}{key, occ{m: m, text: txt}})
		lastIdx[key] = len(order) - 1
	}
	// 3) 折叠非最后一次的出现
	for i, o := range order {
		if i == lastIdx[o.key] || len(order) == 0 {
			continue
		}
		// 同 key 的最新一份若已被 Pass2 折叠（内容变成标记），保留逻辑仍成立：
		// 最新的保留原状或标记均可，旧的统一淘汰
		setContent(o.occ.m, -1, staleMark)
		res.Staled++
		res.SavedChars += int64(len(o.occ.text) - len(staleMark))
	}
}

// foldNearDupToolResults 近重复工具结果折叠：同一工具前后多次调用的结果
// 高度相似（≥90% 的行相同）时，只保留最新一份，旧的换成短标记。
// 与 foldStaleToolResults 互补：那条管"参数完全相同的重复调用"，
// 这条管"参数不同但读到的内容基本没变"（重读改动过的文件、换 offset
// 读同一份大文件等）。链式比对：从最新往回扫，每个旧版本跟它后面
// 最近一个【保留】的版本比，相似则折叠、不相似则自身成为新的参照——
// 这样文件连续演变（A→B→C）时 A 也能被正确折叠。
func foldNearDupToolResults(msgs []any, res *Result) {
	// 1) tool_call_id → 工具名
	idName := map[string]string{}
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := m["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tv := range tcs {
			tc, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			if id == "" || fn == nil {
				continue
			}
			if name, _ := fn["name"].(string); name != "" {
				idName[id] = name
			}
		}
	}
	if len(idName) == 0 {
		return
	}
	// 2) 按工具分组收集够长的 tool 消息（倒序扫描做链式折叠）
	type occ struct {
		m    map[string]any
		text string
	}
	groups := map[string][]occ{}
	var nameOrder []string // 保证输出确定性
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "tool" {
			continue
		}
		id, _ := m["tool_call_id"].(string)
		name, ok := idName[id]
		if !ok {
			continue
		}
		txt, _, ok := contentText(m)
		if !ok || utf8.RuneCountInString(txt) < nearDupMinChars {
			continue
		}
		if txt == staleMark || txt == dedupMark || txt == nearDupMark {
			continue // 已被前面的 pass 折叠过
		}
		if _, seen := groups[name]; !seen {
			nameOrder = append(nameOrder, name)
		}
		groups[name] = append(groups[name], occ{m: m, text: txt})
	}
	// 3) 每组从最新往回链式比对折叠
	for _, name := range nameOrder {
		g := groups[name]
		if len(g) < 2 {
			continue
		}
		ref := g[len(g)-1] // 最新一份永远保留，作为初始参照
		for i := len(g) - 2; i >= 0; i-- {
			o := g[i]
			if lineSimilarity(o.text, ref.text) >= nearDupThreshold {
				setContent(o.m, -1, nearDupMark)
				res.NearDuped++
				res.SavedChars += int64(len(o.text) - len(nearDupMark))
			} else {
				ref = o // 不相似：旧版本成为更旧版本的比对参照
			}
		}
	}
}

// lineSimilarity 行多重集相似度：两条文本按行拆开后，公共行（按出现次数
// 计）占较长一方的比例。O(n) 且对"整份文件 + 少量行改动"高度敏感。
func lineSimilarity(a, b string) float64 {
	la, lb := splitLines(a), splitLines(b)
	if len(la) == 0 || len(lb) == 0 {
		return 0
	}
	if len(la) > nearDupMaxLines {
		la = la[:nearDupMaxLines]
	}
	if len(lb) > nearDupMaxLines {
		lb = lb[:nearDupMaxLines]
	}
	cnt := map[string]int{}
	for _, l := range lb {
		cnt[l]++
	}
	common := 0
	for _, l := range la {
		if cnt[l] > 0 {
			common++
			cnt[l]--
		}
	}
	n := len(la)
	if len(lb) > n {
		n = len(lb)
	}
	return float64(common) / float64(n)
}

// splitLines 按换行拆行并去掉行尾 \r 与首尾空白行（空白差异不应拉低相似度）。
func splitLines(s string) []string {
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimRight(l, "\r")
		lines = append(lines, l)
	}
	// 去掉首尾空行，中间空行保留
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// foldDupImages 重复图片折叠：image_url 完全相同的图片块只保留最新一处，
// 更早的替换为文字标记块。PNG/JPEG 截图动辄上千 token，agent 把同一张
// 截图留在历史里反复回传时浪费明显。
func foldDupImages(msgs []any, res *Result) {
	type ref struct {
		mi, pi int
	}
	var order []struct {
		url string
		r   ref
	}
	lastIdx := map[string]int{}
	for mi, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		arr, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for pi, pv := range arr {
			p, ok := pv.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := p["type"].(string); t != "image_url" {
				continue
			}
			iu, _ := p["image_url"].(map[string]any)
			u, _ := iu["url"].(string)
			if u == "" {
				continue
			}
			order = append(order, struct {
				url string
				r   ref
			}{u, ref{mi, pi}})
			lastIdx[u] = len(order) - 1
		}
	}
	for i, o := range order {
		if i == lastIdx[o.url] {
			continue
		}
		m, ok := msgs[o.r.mi].(map[string]any)
		if !ok {
			continue
		}
		arr, ok := m["content"].([]any)
		if !ok || o.r.pi >= len(arr) {
			continue
		}
		// data:URL 折叠收益最大；http URL 也一并处理（同图重复下载场景）
		arr[o.r.pi] = map[string]any{"type": "text", "text": imgMark}
		res.ImagesDedup++
		res.SavedChars += int64(len(o.url)) / 4 // 图片按 URL 长度粗估（真实视觉 token 无法精确预知）
	}
}

// stripReasoning 剥离历史消息里的 reasoning_content / reasoning 字段。
// 这些字段是模型思考过程的回显，只存在于响应；上游请求不需要
// （本项目所有 provider 均不在请求里读取），客户端误回传时纯属浪费。
func stripReasoning(msgs []any, res *Result) {
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}
		for _, k := range []string{"reasoning_content", "reasoning"} {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok {
					res.SavedChars += int64(len(s))
				}
				delete(m, k)
				res.Stripped++
			}
		}
	}
}

// needsCompact 内容疑似 pretty JSON（含换行或常见缩进空白）且以 { [ 开头。
func needsCompact(s string) bool {
	t := bytes.TrimSpace([]byte(s))
	if len(t) < 2 || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return bytes.ContainsRune(t, '\n') || bytes.Contains(t, []byte(", ")) || bytes.Contains(t, []byte(": "))
}

// compactJSON 压平 JSON（仅删语法空白，字符串值原样）。非合法 JSON 返回 false。
func compactJSON(s string) (string, bool) {
	var buf bytes.Buffer
	buf.Grow(len(s))
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return "", false
	}
	return buf.String(), true
}

// contentText 取消息文本。content 为字符串返回 (s, -1, true)；
// 为数组且恰含一个 text 块返回 (text, 0, true)。其余不支持。
func contentText(m map[string]any) (string, int, bool) {
	switch c := m["content"].(type) {
	case string:
		return c, -1, true
	case []any:
		if len(c) != 1 {
			return "", 0, false
		}
		b, ok := c[0].(map[string]any)
		if !ok {
			return "", 0, false
		}
		if t, ok := b["type"].(string); ok && t == "text" {
			if s, ok := b["text"].(string); ok {
				return s, 0, true
			}
		}
		return "", 0, false
	}
	return "", 0, false
}

// setContent 写回消息文本（idx<0 为字符串 content）。
func setContent(m map[string]any, idx int, v string) {
	if idx < 0 {
		m["content"] = v
		return
	}
	if arr, ok := m["content"].([]any); ok && len(arr) == 1 {
		if b, ok := arr[0].(map[string]any); ok {
			b["text"] = v
		}
	}
}

// marshalCompact 紧凑序列化。SetEscapeHTML(false)：不要把 < > & 转成
// \u003c 之类——agent 内容里全是 XML 标签和 diff，HTML 转义会膨胀体积。
func marshalCompact(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
