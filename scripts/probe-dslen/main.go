// probe-dslen：探测 DeepSeek 网页版 prompt 长度上限（临时诊断工具）。
// 用法：go run ./scripts/probe-dslen -data data -sizes 10000,60000,120000,240000
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/provider"
	"tokenhub/internal/provider/deepseek"
	"tokenhub/internal/store"
)

func randHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// discardSink 吃掉全部输出，只关心错误返回。
type discardSink struct{}

func (discardSink) Delta(provider.Delta)        {}
func (discardSink) Finish(provider.Finish)      {}
func (discardSink) StreamError(int64, string)   {}
func (discardSink) Close()                      {}

func main() {
	dataDir := flag.String("data", "data", "data 目录")
	sizesFlag := flag.String("sizes", "10000,60000,120000,240000", "探测的字符数，逗号分隔")
	styleFlag := flag.String("style", "zh", "内容风格：zh=中文填充 hex=随机哈希多行 hexline=单行哈希 md5=全随机多行 multi=多轮中文 tools=工具结构中文 rand=工具结构随机hex")
	turnsFlag := flag.Int("turns", 0, "multi/tools/rand 风格的轮数（每轮内容均分总长度）")
	flag.Parse()

	cfg, err := config.Load(*dataDir)
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	st, err := store.Load(*dataDir)
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	accs := st.ByProvider("deepseek")
	if len(accs) == 0 {
		fmt.Println("没有 deepseek 账号")
		return
	}
	acct := accs[0]
	p := deepseek.New(cfg)

	var sizes []int
	for _, s := range strings.Split(*sizesFlag, ",") {
		var n int
		fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
		if n > 0 {
			sizes = append(sizes, n)
		}
	}

	multiTurns := *turnsFlag
	if (*styleFlag == "multi" || *styleFlag == "tools" || *styleFlag == "rand") && multiTurns <= 0 {
		multiTurns = 30
	}

	for _, n := range sizes {
		var messages []map[string]any
		if *styleFlag == "multi" {
			// N 轮中文对话（测轮次上限），每轮内容唯一
			per := n/multiTurns + 1
			for i := 0; i < multiTurns; i++ {
				h := randHex() + randHex()
				msgs_i := fmt.Sprintf("第 %d 轮。背景资料：%s\n请简要确认收到。", i, h[:24]+strings.Repeat("这是第"+fmt.Sprint(i)+"轮的异质上下文内容。", per/24+1))
				if len(msgs_i) > per {
					msgs_i = msgs_i[:per-per%3]
				}
				messages = append(messages, map[string]any{"role": "user", "content": msgs_i})
				messages = append(messages, map[string]any{"role": "assistant", "content": fmt.Sprintf("第 %d 轮已确认，要点编号 %s。", i, h[24:36])})
			}
			messages = append(messages, map[string]any{"role": "user", "content": "综合以上所有轮次，请只回复两个字：好的"})
		} else if *styleFlag == "tools" || *styleFlag == "rand" {
			// N 对工具调用结构：assistant(tool_call) + tool(响应内容)
			// tools=中文响应 rand=随机hex响应（复现网关失败场景）
			per := n/multiTurns + 1
			for i := 0; i < multiTurns; i++ {
				messages = append(messages, map[string]any{"role": "assistant", "content": nil,
					"tool_calls": []map[string]any{{"id": fmt.Sprintf("call_%d", i), "type": "function",
						"function": map[string]any{"name": "run_cmd", "arguments": fmt.Sprintf("{\"command\":\"analyze module_%d\"}", i)}}}})
				var sb strings.Builder
				for sb.Len() < per {
					h := randHex() + randHex()
					if *styleFlag == "rand" {
						sb.WriteString(fmt.Sprintf("[%s] process <pid=%d> file_%d.go && path=\"C:/src/pkg\" state=%s\n", h[:12], i%99999, i, h[12:44]))
					} else {
						ms := int(h[24]) % 5000
						sb.WriteString(fmt.Sprintf("模块%d进程%d处理文件%d，路径仓库源码目录%s，状态正常完成，耗时%d毫秒\n", i, i%99999, i, h[12:24], ms))
					}
				}
				out := sb.String()
				if len(out) > per {
					out = out[:per-per%4]
				}
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("call_%d", i), "content": out})
			}
			messages = append(messages, map[string]any{"role": "user", "content": "总结以上所有模块，请只回复两个字：好的"})
		} else {
			var content string
			switch *styleFlag {
			case "md5":
				// 全随机 md5 hex 多行（复现网关失败案例的内容形状）
				var sb strings.Builder
				line := 0
				for sb.Len() < n {
					h := fmt.Sprintf("%s%s", randHex(), randHex())
					sb.WriteString(fmt.Sprintf("[%s] process <pid=%d> file_%d.go && path=\"C:/src/pkg\" state=%s\n",
						h[:12], line%99999, line, h[12:44]))
					line++
				}
				content = sb.String()[:min(n, sb.Len())]
			case "hex":
				// 随机哈希多行（token 密度极高，模拟代码/日志）
				var sb strings.Builder
				line := 0
				for sb.Len() < n {
					sb.WriteString(fmt.Sprintf("[%016x] module func_%d call && path=\"C:/src/pkg\" state=0x%s done\n",
						time.Now().UnixNano()%1<<62, line, strings.Repeat("abcdef0123456789", 2)))
					line++
				}
				content = sb.String()[:min(n, sb.Len())]
			case "hexline":
				// 单行随机哈希（无换行）
				var sb strings.Builder
				for sb.Len() < n {
					sb.WriteString("0123456789abcdef0123456789abcdef")
				}
				content = sb.String()[:n]
			default:
				// 中文填充
				fill := strings.Repeat("这是用于长度探测的填充文本，无实际含义。", n/20+1)
				if len(fill) < n {
					fill += strings.Repeat("测", n)
				}
				content = fill[:n]
			}
			content += "\n\n请只回复两个字：好的"
			messages = []map[string]any{{"role": "user", "content": content}}
		}
		req := map[string]any{
			"model":    "deepseek-chat",
			"messages": messages,
		}
		b, _ := json.Marshal(req)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		err := p.Chat(ctx, acct, b, discardSink{})
		cancel()
		result := "成功"
		if err != nil {
			result = err.Error()
		}
		fmt.Printf("size=%6d chars turns=%d  ->  %s\n", n, max(1, len(messages)), result)
	}
}
