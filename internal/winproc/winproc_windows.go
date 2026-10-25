// Package winproc Windows 子进程辅助：统一隐藏控制台窗口。
// 主程序为 windowsgui 无控制台，直接 exec 控制台程序（taskkill/reg/powershell 等）
// 会闪烁黑色 CMD 窗口；统一加 CREATE_NO_WINDOW 消除。
package winproc

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Cmd 返回隐藏控制台窗口的 exec.Cmd。
func Cmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}
