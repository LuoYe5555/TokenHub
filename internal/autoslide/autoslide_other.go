//go:build !windows

// Package autoslide 非 Windows 平台的空实现：
// 自动滑块依赖 user32 SendInput（仅 Windows）。面板在非窗口模式下
// 不会调用这里（webviewWindow=false，前端只提供手动领取入口）。
package autoslide

import "errors"

// FocusWindow 非 Windows 平台无操作。
func FocusWindow(hwnd uintptr) {}

// Drag 非 Windows 平台不支持模拟滑块。
func Drag(x1, y1, x2, y2 int) error {
	return errors.New("自动滑块仅支持 Windows 平台，请手动完成滑块验证")
}
