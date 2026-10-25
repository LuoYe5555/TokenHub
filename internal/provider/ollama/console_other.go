//go:build !windows

package ollama

import "os/exec"

// hideServeConsole 非 Windows 平台无需隐藏控制台。
func hideServeConsole(cmd *exec.Cmd) { _ = cmd }
