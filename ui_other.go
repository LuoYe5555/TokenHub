//go:build !windows

// 非 Windows 平台：无 WebView2 窗口模式，面板用系统浏览器访问。
package main

import (
	"context"
	"net/http"
	"os"

	"tokenhub/internal/web"
)

// runWindowMode 非 Windows 平台永远返回 false（纯后台服务 / -browser 浏览器模式）。
func runWindowMode(_ *web.Panel, _, _ string, _ <-chan os.Signal, _ context.Context, _ *http.Server, _ bool) bool {
	return false
}

// messageBox 非 Windows 平台无弹窗（winFatal 已打印 stderr 与日志）。
func messageBox(_, _ string) {}
