//go:build !windows

package qwencdp

import (
	"os"
	"os/exec"
	"runtime"
)

// FindBrowser 在 Linux / macOS 上找 Chrome 系浏览器。
// 优先用 QWEN_BROWSER 环境变量指定，其次 PATH 里的常见命令名，
// 最后回退到各发行版 / macOS 的固定安装路径。
func FindBrowser() string {
	if v := os.Getenv("QWEN_BROWSER"); v != "" && fileExists(v) {
		return v
	}
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"microsoft-edge", "microsoft-edge-stable", "brave-browser", "chrome",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	cands := []string{
		"/opt/google/chrome/chrome",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
		"/var/lib/flatpak/exports/bin/com.google.Chrome",
	}
	if runtime.GOOS == "darwin" {
		cands = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// hideWindow 非 Windows 平台无需隐藏控制台窗口。
func hideWindow(cmd *exec.Cmd) {}
