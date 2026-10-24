package zcode

// 对照实验（环境开关 ZCODE_TS_LIVE=1 门控）：
// 1) TestTSHintLive      —— 验证「给 ToolSearch 的 description 追加说明」是否触发上游 3007 风控
// 2) TestTSHintBehaviorLive —— 干净 A/B：截取真实快照第一轮（无毒化历史），
//    对比 tsHint 开/关时模型第一反应是直接调文件工具还是先跑 ToolSearch。

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/store"
)

func tsLiveAccount(t *testing.T) *store.Account {
	t.Helper()
	dataDir := os.Getenv("TOKENHUB_DATA")
	if dataDir == "" {
		dataDir = `C:\Users\30955\Desktop\TokensAPI\data`
	}
	st, err := store.Load(dataDir)
	if err != nil {
		t.Fatalf("加载账号失败: %v", err)
	}
	for _, a := range st.All() {
		if a.Provider == store.PZCode && strings.TrimSpace(a.AccessToken) != "" {
			return a
		}
	}
	t.Skip("没有可用 zcode 账号")
	return nil
}

func TestTSHintLive(t *testing.T) {
	if os.Getenv("ZCODE_TS_LIVE") == "" {
		t.Skip("需要 ZCODE_TS_LIVE=1")
	}
	acct := tsLiveAccount(t)

	cfg := &config.Config{}
	cfg.Normalize()
	openaiReq := []byte(`{
		"model": "glm-5.3-flash",
		"stream": false,
		"messages": [
			{"role": "user", "content": "Reply with exactly two characters: OK"}
		],
		"tools": [
			{"type": "function", "function": {"name": "ToolSearch", "description": "Search for tools by keywords to load their schemas.", "parameters": {"type": "object", "properties": {"queries": {"type": "array", "items": {"type": "string"}}}, "required": ["queries"]}}},
			{"type": "function", "function": {"name": "Bash", "description": "Executes a bash command and returns output.", "parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}}}
		]
	}`)

	send := func(tsHint bool) (int, string) {
		body, err := BuildAnthropicFromOpenAI(openaiReq, "glm-5.3-flash", 256)
		if err != nil {
			t.Fatalf("形状转换失败: %v", err)
		}
		applyShape(body, cfg.ZCodeInjectShape, stableSessionHex(acct.ID), false, tsHint, false)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		h := planMessagesHeaders(acct.AccessToken)
		if mid := acct.ExtraGet(store.ExtraZCodeDeviceMid); mid != "" {
			h["X-Device-Mid"] = mid
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		resp, err := httpx.DoStream(ctx, "POST", apiBase(cfg.PC(store.PZCode))+"/api/v1/zcode-plan/anthropic/v1/messages", h, raw, cfg.ConnectTimeout())
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		if resp.Stream != nil {
			defer resp.Stream.Close()
		}
		return resp.Status, string(resp.Body)
	}

	status, body := send(false)
	t.Logf("基线(tsHint=false): status=%d body=%s", status, body)
	if status >= 400 {
		t.Fatalf("基线就失败（%d），上游当前不可用，实验结果不可信，稍后再试", status)
	}

	status2, body2 := send(true)
	t.Logf("实验(tsHint=true):  status=%d body=%s", status2, body2)
	if status2 >= 400 {
		t.Logf(">>> 结论：工具定义也参与风控校验，ToolSearch 描述补丁不可行（3007）")
	} else {
		t.Logf(">>> 结论：工具定义不触发风控，zcodeTsHint 可作为一劳永逸方案")
	}
}

func TestTSHintBehaviorLive(t *testing.T) {
	if os.Getenv("ZCODE_TS_LIVE") == "" {
		t.Skip("需要 ZCODE_TS_LIVE=1")
	}
	acct := tsLiveAccount(t)
	snapPath := os.Getenv("ZCODE_SNAPSHOT")
	if snapPath == "" {
		snapPath = `C:\Users\30955\Desktop\TokensAPI\data\reqdump\1024-183541-zcode-glm-5-3.json`
	}
	raw, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	var snap map[string]any
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	// 快照 model 带 "zcode:" 前缀（网关路由层负责剥掉），直连上游需自行剥除
	if m, _ := snap["model"].(string); strings.HasPrefix(m, "zcode:") {
		snap["model"] = strings.TrimPrefix(m, "zcode:")
	}
	// 只保留 system + 第一条 user 消息（干净历史，无 ToolSearch 毒化锚定）
	if msgs, ok := snap["messages"].([]any); ok && len(msgs) > 2 {
		snap["messages"] = msgs[:2]
	}
	raw, err = json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}

	var lastStream []byte
	run := func(tsHint, stripTS bool) map[string]int {
		body, err := BuildAnthropicFromOpenAI(raw, "glm-5.3", 16384)
		if err != nil {
			t.Fatalf("形状转换失败: %v", err)
		}
		applyShape(body, "overwrite", stableSessionHex(acct.ID), false, tsHint, stripTS)
		b, _ := json.Marshal(body)
		h := planMessagesHeaders(acct.AccessToken)
		if mid := acct.ExtraGet(store.ExtraZCodeDeviceMid); mid != "" {
			h["X-Device-Mid"] = mid
		}
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		resp, err := httpx.DoStream(ctx, "POST", apiBase(nil)+"/api/v1/zcode-plan/anthropic/v1/messages", h, b, 15*time.Second)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		if resp.Stream != nil {
			defer resp.Stream.Close()
		}
		if resp.Status >= 400 {
			t.Fatalf("status=%d body=%s", resp.Status, string(resp.Body[:min(len(resp.Body), 200)]))
		}
		stream, _ := io.ReadAll(resp.Stream)
		lastStream = stream
		// 解析 Anthropic SSE：content_block_start 事件里 content_block.type=tool_use 带 name
		names := map[string]int{}
		for _, line := range strings.Split(string(stream), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev struct {
				Type         string `json:"type"`
				ContentBlock struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if json.Unmarshal([]byte(line[6:]), &ev) != nil {
				continue
			}
			if ev.Type == "content_block_start" && ev.ContentBlock.Type == "tool_use" {
				names[ev.ContentBlock.Name]++
			}
		}
		return names
	}

	off := run(false, false)
	t.Logf("A组(无补丁): 第一反应工具调用 = %v", off)
	time.Sleep(30 * time.Second)
	on := run(true, true)
	t.Logf("B组(strip+hint): 第一反应工具调用 = %v", on)
	_ = os.WriteFile(`C:\Users\30955\Desktop\TokensAPI\.workbuddy\tmp\strip_stream.txt`, lastStream, 0o644)
	direct := func(m map[string]int) bool {
		return m["Bash"]+m["Read"]+m["Glob"]+m["Grep"]+m["Write"] > 0
	}
	t.Logf(">>> A组直接调文件工具=%v, B组(strip)直接调文件工具=%v", direct(off), direct(on))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
