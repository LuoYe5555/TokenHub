//go:build !windows

// Package autoslide 非 Windows 平台的占位实现。
//
// Windows 版通过 SendInput 在系统层模拟真人滑块拖拽（配合 WebView2 窗口过
// 阿里云风控）；Linux / macOS 没有等价的可移植实现，这里统一返回明确错误，
// 由调用方提示用户手动完成验证。
package autoslide

import "errors"

// ErrUnsupported 当前平台不支持系统级鼠标模拟。
var ErrUnsupported = errors.New("自动滑块仅支持 Windows，请在弹出的浏览器窗口里手动完成验证")

// FocusWindow 非 Windows 平台无操作。
func FocusWindow(hwnd uintptr) {}

// Drag 非 Windows 平台直接返回不支持。
func Drag(x1, y1, x2, y2 int) error { return ErrUnsupported }
