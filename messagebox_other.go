//go:build !windows

package main

// messageBox 非 Windows 平台没有原生弹窗，错误只走日志与 stderr（见 winFatal）。
func messageBox(title, text string) {}
