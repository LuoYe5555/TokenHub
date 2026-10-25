//go:build !windows

package winproc

import "os/exec"

// Cmd 返回普通 exec.Cmd（非 Windows 平台无控制台窗口问题）。
func Cmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
