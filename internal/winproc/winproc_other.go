//go:build !windows

// Package winproc 子进程辅助。非 Windows 平台没有「控制台窗口闪烁」问题，
// 直接透传给 exec.Command，保持调用方代码一致。
package winproc

import "os/exec"

// Cmd 返回 exec.Cmd（非 Windows 无需隐藏窗口）。
func Cmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
