// 风控签名嗅探：连上页面 target 监听 Network.requestWillBeSent，
// 抓一条带 bx-ua 的 /api/ 请求头。阿里 WAF 的 bx-ua / bx-umidtoken
// 由站点 JS 生成，模拟头（纯 UA 字符串）是拿不到信任分的——
// 抓真实浏览器的来复用，配合全套 cookie 让请求形状贴近浏览器。
package qwencdp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SniffAPIHeaders 监听页面网络事件，返回第一条带 bx-ua 的 API 请求头
// （key 全小写）。reload=true 时先刷新页面——Network.enable 只能看到
// 之后发出的请求，而页面加载必然打 /api/ 请求（auths/models/chats…）。
// 抓不到（超时/无页面）返回 nil，调用方回退模拟头。
func SniffAPIHeaders(port int, hostPart string, wait time.Duration, reload bool) map[string]string {
	type target struct {
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var targets []target
	if json.NewDecoder(resp.Body).Decode(&targets) != nil {
		return nil
	}
	var page *target
	for i := range targets {
		if targets[i].Type == "page" && strings.Contains(targets[i].URL, hostPart) {
			page = &targets[i]
			break
		}
	}
	if page == nil || page.WebSocketDebuggerURL == "" {
		return nil
	}
	ws, err := wsDial(page.WebSocketDebuggerURL)
	if err != nil {
		return nil
	}
	defer ws.Close()
	if _, err := ws.Call(1, "Network.enable", map[string]any{}, 5*time.Second); err != nil {
		return nil
	}
	if reload {
		// 失败不致命：页面若刚好有轮询请求也能抓到
		_, _ = ws.Call(2, "Page.reload", map[string]any{"ignoreCache": false}, 5*time.Second)
	}
	ws.c.SetReadDeadline(time.Now().Add(wait))
	for {
		data, err := ws.readMessage()
		if err != nil {
			return nil
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Request struct {
					URL     string            `json:"url"`
					Headers map[string]string `json:"headers"`
				} `json:"request"`
			} `json:"params"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Method != "Network.requestWillBeSent" {
			continue
		}
		if !strings.Contains(msg.Params.Request.URL, "/api/") {
			continue
		}
		h := make(map[string]string, len(msg.Params.Request.Headers))
		for k, v := range msg.Params.Request.Headers {
			h[strings.ToLower(k)] = v
		}
		if h["bx-ua"] == "" {
			continue // 静态资源/无签名请求不要
		}
		return h
	}
}
