//go:build !windows

package main

import (
	"context"
	"net/http"
	"os"

	"tokenhub/internal/web"
)

// canWindow 非 Windows 平台没有 WebView2，不启用内嵌窗口：
// 默认纯后台服务（面板用浏览器访问），-browser 可自动打开系统浏览器。
func canWindow() bool { return false }

// runAppWindow 非 Windows 平台不会走到这里（canWindow 恒为 false），
// 保留空实现是为了让 main.go 的调用点在两个平台都成立。
func runAppWindow(baseURL, dataDir string, panel *web.Panel, quit <-chan os.Signal, ctx context.Context, srv *http.Server) {
}
