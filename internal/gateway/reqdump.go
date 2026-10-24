package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tokenhub/internal/logx"
)

// dumpReq 诊断用：把客户端发来的原始请求体落盘（cfg.ReqDump 开启时）。
// 用于排查「模型说找不到工具」这类客户端侧问题——看工具清单到底有没有随请求发过来。
// 只保留最近 reqDumpKeep 份，避免占用磁盘。
func (g *Gateway) dumpReq(model string, body []byte) {
	if g.cfg == nil || !g.cfg.ReqDump || g.cfg.DataDir == "" {
		return
	}
	dir := filepath.Join(g.cfg.DataDir, "reqdump")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	// 概要日志：不落内容也能看出关键信息（工具数/名单、消息数）
	var peek struct {
		Tools json.RawMessage `json:"tools"`
		Msgs  json.RawMessage `json:"messages"`
	}
	toolsN, msgsN := 0, 0
	names := []string{}
	if json.Unmarshal(body, &peek) == nil {
		if len(peek.Tools) > 0 {
			var arr []json.RawMessage
			_ = json.Unmarshal(peek.Tools, &arr)
			toolsN = len(arr)
			var toolsArr []map[string]any
			if json.Unmarshal(peek.Tools, &toolsArr) == nil {
				for _, t := range toolsArr {
					if fn, ok := t["function"].(map[string]any); ok {
						if n, _ := fn["name"].(string); n != "" {
							names = append(names, n)
						}
					} else if n, _ := t["name"].(string); n != "" {
						names = append(names, n) // Responses/Anthropic 风格
					}
				}
			}
		}
		if len(peek.Msgs) > 0 {
			var arr []json.RawMessage
			_ = json.Unmarshal(peek.Msgs, &arr)
			msgsN = len(arr)
		}
	}
	logx.Infof("gateway", "请求快照(%s): messages=%d tools=%d [%s]", model, msgsN, toolsN, strings.Join(names, ","))

	ts := time.Now().Format("0102-150405")
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/:*?"<>| .`, r) {
			return '-'
		}
		return r
	}, model)
	name := filepath.Join(dir, ts+"-"+safe+".json")
	_ = os.WriteFile(name, body, 0o644)

	// 清理旧快照，只留 8 份
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(ents) <= 8 {
		return
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, e := range ents[:len(ents)-8] {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
