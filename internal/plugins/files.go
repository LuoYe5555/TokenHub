package plugins

import (
	"os"
	"path/filepath"
)

// readFileBytes 以 root+rel 读取文件（rel 已在调用前做过穿越校验）。
func readFileBytes(root, rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
}

// writeFileBytes 以 root+rel 写文件。
func writeFileBytes(root, rel string, data []byte) error {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
