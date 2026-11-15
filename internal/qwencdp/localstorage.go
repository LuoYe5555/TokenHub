// CDP 读页面 localStorage：登录态存在 localStorage（而非 cookie）的站点用
// （如 DeepSeek 网页版 userToken）。没有匹配页面时自动开一个新标签导航过去。
package qwencdp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type pageTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// findPageTarget 找 URL 含 hostPart 的页面 target；没有时通过浏览器开一个。
func findPageTarget(port int, hostPart, url string) (*pageTarget, error) {
	for attempt := 0; attempt < 2; attempt++ {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
		if err != nil {
			return nil, err
		}
		var list []pageTarget
		if err := json.NewDecoder(resp.Body).Decode(&list); err == nil {
			resp.Body.Close()
			for i := range list {
				t := &list[i]
				if t.Type == "page" && strings.Contains(t.URL, hostPart) && t.WebSocketDebuggerURL != "" {
					return t, nil
				}
			}
		} else {
			resp.Body.Close()
		}
		if attempt == 0 && url != "" {
			// 没有匹配页面：浏览器级 target 开一个新标签
			var ver struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			vresp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
			if err != nil {
				return nil, err
			}
			if err := json.NewDecoder(vresp.Body).Decode(&ver); err != nil {
				vresp.Body.Close()
				return nil, err
			}
			vresp.Body.Close()
			if ver.WebSocketDebuggerURL == "" {
				return nil, errors.New("调试接口未返回 webSocketDebuggerUrl")
			}
			ws, err := wsDial(ver.WebSocketDebuggerURL)
			if err != nil {
				return nil, err
			}
			_, err = ws.Call(1, "Target.createTarget", map[string]any{"url": url}, 15*time.Second)
			ws.Close()
			if err != nil {
				return nil, err
			}
			time.Sleep(1500 * time.Millisecond) // 等标签出现
			continue
		}
		return nil, fmt.Errorf("未找到 %s 的页面 target", hostPart)
	}
	return nil, fmt.Errorf("未找到 %s 的页面 target", hostPart)
}

// GetLocalStorageValue 在目标站点的页面上执行 localStorage.getItem(key)。
// 页面尚未就绪/值为空时在 timeout 内轮询。返回空串表示超时未取到。
func GetLocalStorageValue(port int, hostPart, url, key string, timeout time.Duration) (string, error) {
	t, err := findPageTarget(port, hostPart, url)
	if err != nil {
		return "", err
	}
	ws, err := wsDial(t.WebSocketDebuggerURL)
	if err != nil {
		return "", err
	}
	defer ws.Close()
	expr := fmt.Sprintf("(function(){try{return window.localStorage.getItem(%q)||''}catch(e){return ''}})()", key)
	deadline := time.Now().Add(timeout)
	var id int
	var lastErr error
	for {
		id++
		raw, err := ws.Call(id, "Runtime.evaluate", map[string]any{
			"expression":    expr,
			"returnByValue": true,
		}, 10*time.Second)
		if err == nil {
			var out struct {
				Result struct {
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &out) == nil && out.Result.Value != "" {
				return out.Result.Value, nil
			}
			lastErr = errors.New("evaluate 成功但值为空")
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return "", fmt.Errorf("localStorage 轮询超时: %w", lastErr)
			}
			return "", nil
		}
		time.Sleep(time.Second)
	}
}
