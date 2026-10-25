//go:build !windows

package qwencdp

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// FindBrowser 非 Windows 平台找 Chrome/Chromium/Edge：
// 先查 PATH，再试常见安装路径。
func FindBrowser() string {
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"microsoft-edge", "microsoft-edge-stable", "chrome",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default: // linux
		home := os.Getenv("HOME")
		cands = []string{
			"/opt/google/chrome/chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
			"/usr/bin/microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
			"/snap/bin/chromium",
			filepath.Join(home, ".local/bin/chrome"),
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// setSysProcAttr 非 Windows 平台无需隐藏窗口。
func setSysProcAttr(cmd *exec.Cmd, flags int) {}
