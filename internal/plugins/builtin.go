package plugins

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed builtin
var builtinFS embed.FS

// ShelfItem 货架条目。
type ShelfItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      string `json:"author,omitempty"`
	Builtin     bool   `json:"builtin"` // 内置示例，离线可装
	Repo        string `json:"repo,omitempty"`
}

// Shelf 内置货架（后续可扩展远程索引）。
func (m *Manager) Shelf() []ShelfItem {
	var out []ShelfItem
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(builtinFS, "builtin/"+e.Name()+"/plugin.json")
		if err != nil {
			continue
		}
		var man Manifest
		if json.Unmarshal(raw, &man) != nil {
			continue
		}
		out = append(out, ShelfItem{
			ID: man.ID, Name: man.Name, Description: man.Description,
			Version: man.Version, Author: man.Author, Builtin: true,
		})
	}
	return out
}

// InstallBuiltin 安装内置示例插件。
func (m *Manager) InstallBuiltin(id string) (*Manifest, error) {
	if !idRe.MatchString(id) {
		return nil, fmt.Errorf("id 不合法")
	}
	root := "builtin/" + id
	if _, err := fs.ReadFile(builtinFS, root+"/plugin.json"); err != nil {
		return nil, fmt.Errorf("内置货架没有这个插件: %s", id)
	}
	tmp := filepath.Join(m.root, ".tmp-builtin", id)
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	err := fs.WalkDir(builtinFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(path, "builtin/"), id+"/")
		data, err := fs.ReadFile(builtinFS, path)
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		return nil, err
	}
	return m.InstallDir(tmp, "builtin:"+id)
}
