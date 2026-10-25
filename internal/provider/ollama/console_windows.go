//go:build windows

package ollama

import (
	"os/exec"
	"syscall"
)

// hideServeConsole Windows GUI 程序拉起控制台子进程时防闪黑框。
func hideServeConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
