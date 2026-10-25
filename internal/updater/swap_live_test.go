package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDownloadSwapLive 真实下载 release 资产并在临时目录验证替换逻辑（不触碰真实 exe）。
func TestDownloadSwapLive(t *testing.T) {
	if os.Getenv("UPD_LIVE") == "" {
		t.Skip("需要 UPD_LIVE=1")
	}
	dir := t.TempDir()
	m := &Manager{exePath: filepath.Join(dir, "TokenHub.exe")}
	// 假旧 exe（>0 字节即可，替换逻辑不校验旧文件 PE 头）
	os.WriteFile(m.exePath, []byte("old-exe-placeholder"), 0o755)

	_, url, err := m.latest()
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if err := m.downloadAndSwap(url); err != nil {
		t.Fatalf("downloadAndSwap: %v", err)
	}
	newBin, err := os.ReadFile(m.exePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(newBin) < 5<<20 || newBin[0] != 'M' {
		t.Fatalf("新 exe 异常: %d 字节", len(newBin))
	}
	prev, err := os.ReadFile(filepath.Join(dir, "TokenHub.prev.exe"))
	if err != nil || string(prev) != "old-exe-placeholder" {
		t.Fatalf("旧 exe 备份异常: %v", err)
	}
	t.Logf("全链路 OK: 新 exe %d 字节, 旧 exe 已备份为 TokenHub.prev.exe", len(newBin))
}
