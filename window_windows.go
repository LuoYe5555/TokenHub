//go:build windows

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	webview "github.com/jchv/go-webview2"

	"tokenhub/internal/appicon"
	"tokenhub/internal/config"
	"tokenhub/internal/logx"
	"tokenhub/internal/web"
	"tokenhub/internal/winicon"
)

// canWindow Windows 有 WebView2 内嵌窗口。
func canWindow() bool { return true }

// runAppWindow 创建 WebView2 窗口承载面板，阻塞到窗口关闭后优雅退出。
func runAppWindow(baseURL, dataDir string, panel *web.Panel, quit <-chan os.Signal, ctx context.Context, srv *http.Server) {
	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  config.AppName,
			Width:  1220,
			Height: 840,
			Center: true,
		},
	})
	w.SetTitle(config.AppName)
	w.Dispatch(func() { w.Navigate(baseURL) })
	defer w.Destroy()
	go func() {
		select {
		case <-quit:
			w.Terminate()
		case <-ctx.Done():
			w.Terminate()
		}
	}()
	applyWindowIcon(w, dataDir)
	// webview 桥：自动领取撞验证码时自动弹窗 + SendInput 自动滑块（全程免人工）
	panel.SetWebview(w.Dispatch, func(js string) { w.Eval(js) }, uintptr(w.Window()))
	w.Run() // 阻塞直到窗口关闭
	logx.Infof("main", "窗口已关闭，正在退出…")
	shutdown(srv)
}

// applyWindowIcon 图标已全部内置：标题栏用内置 app.png（用户自定义狗图），
// 任务栏固定用内置品牌 "T" 图。不再支持外部图片路径配置。
func applyWindowIcon(w webview.WebView, dataDir string) {
	smallImg, err := appicon.Image()
	if err != nil {
		logx.Warnf("main", "内置标题栏图解码失败: %v", err)
		return
	}
	bigImg, err := appicon.BrandImage()
	if err != nil {
		logx.Warnf("main", "内置品牌图解码失败: %v", err)
		bigImg = smallImg
	}
	if smallImg == nil || bigImg == nil {
		return
	}
	smallPath := filepath.Join(dataDir, "window-icon.ico")
	if err := winicon.WriteICOFile(smallPath, smallImg); err != nil {
		logx.Warnf("main", "窗口图标生成失败: %v", err)
		return
	}
	bigPath := filepath.Join(dataDir, "taskbar-icon.ico")
	if err := winicon.WriteICOFile(bigPath, bigImg); err != nil {
		logx.Warnf("main", "任务栏图标生成失败: %v", err)
		return
	}
	// 窗口已创建（NewWithOptions 内部完成），同线程 SendMessage 不依赖消息循环
	if err := winicon.Apply(w.Window(), smallPath, bigPath); err != nil {
		logx.Warnf("main", "窗口图标设置失败: %v", err)
	} else {
		logx.Infof("main", "窗口图标已应用（标题栏 内置 / 任务栏 T）")
	}
}
