//go:build windows

package qwencdp

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const createNoWindow = 0x08000000

// FindBrowser 找系统 Edge / Chrome（环境变量路径 + 注册表 App Paths 兜底）。
func FindBrowser() string {
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	lpf := os.Getenv("LocalAppData")
	cands := []string{
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(lpf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(lpf, `Google\Chrome\Application\chrome.exe`),
	}
	for _, c := range cands {
		if c != "" && fileExists(c) {
			return c
		}
	}
	// 环境变量缺失（精简系统/受限环境）时从注册表 App Paths 找
	for _, name := range []string{`msedge.exe`, `chrome.exe`} {
		if p := regAppPath(name); p != "" && fileExists(p) {
			return p
		}
	}
	return ""
}

// regAppPath 查 HKLM/HKCU 的 App Paths 注册表项。
func regAppPath(exeName string) string {
	for _, root := range []string{`SOFTWARE`, `SOFTWARE\WOW6432Node`} {
		for _, hive := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
			k, err := registry.OpenKey(hive, ``+root+`\Microsoft\Windows\CurrentVersion\App Paths\`+exeName, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			v, _, err := k.GetStringValue("")
			k.Close()
			if err == nil && v != "" {
				return v
			}
		}
	}
	return ""
}

// hideWindow 以无控制台窗口方式启动浏览器（主程序是 GUI，否则会闪黑框）。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
