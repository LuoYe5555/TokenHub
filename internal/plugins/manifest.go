// Package plugins TokenHub 插件系统：goja 沙箱插件 + 面板卡片页贡献。
// 插件包结构：plugin.json 清单 + main.js 入口（goja 执行，定义全局
// onRpc(method, params)）+ ui/index.html 卡片页（iframe sandbox 渲染）。
//
// 存储布局（DataDir/plugins/）：installed/<id>/ 安装文件；installed.json
// 注册表（启用状态/来源/时间）；data/<id>.json 插件键值存储；audit.jsonl 审计。
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,47}$`)
var verRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Manifest 插件清单（plugin.json）。
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	Main        string `json:"main"`      // 入口 JS（相对包根）
	Permissions struct {
		Network []string `json:"network,omitempty"` // 允许出站的精确 origin 白名单；空 = 禁网
		Storage bool     `json:"storage,omitempty"` // 允许键值存储
	} `json:"permissions"`
	UI struct {
		Entry string `json:"entry,omitempty"` // 卡片页 HTML（相对包根，须在 ui/ 下）
		Title string `json:"title,omitempty"`
	} `json:"ui"`
}

// LoadManifest 从目录读 plugin.json 并校验。
func LoadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return nil, fmt.Errorf("缺少 plugin.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("plugin.json 解析失败: %w", err)
	}
	if !idRe.MatchString(m.ID) {
		return nil, fmt.Errorf("id 不合法（须匹配 %s）: %q", idRe.String(), m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("缺少 name")
	}
	if !verRe.MatchString(m.Version) {
		return nil, fmt.Errorf("version 须为 x.y.z: %q", m.Version)
	}
	if m.Main == "" {
		m.Main = "main.js"
	}
	if err := safeRel(m.Main); err != nil {
		return nil, fmt.Errorf("main: %w", err)
	}
	if !strings.HasSuffix(m.Main, ".js") {
		return nil, fmt.Errorf("main 须为 .js 文件")
	}
	if _, err := os.Stat(filepath.Join(dir, m.Main)); err != nil {
		return nil, fmt.Errorf("入口文件不存在: %s", m.Main)
	}
	if m.UI.Entry != "" {
		if err := safeRel(m.UI.Entry); err != nil {
			return nil, fmt.Errorf("ui.entry: %w", err)
		}
		if !strings.HasPrefix(m.UI.Entry, "ui/") || !strings.HasSuffix(m.UI.Entry, ".html") {
			return nil, fmt.Errorf("ui.entry 必须是 ui/ 下的 .html 文件")
		}
		if _, err := os.Stat(filepath.Join(dir, m.UI.Entry)); err != nil {
			return nil, fmt.Errorf("卡片页不存在: %s", m.UI.Entry)
		}
	}
	for i, o := range m.Permissions.Network {
		o = strings.TrimSuffix(strings.TrimSpace(o), "/")
		if !strings.HasPrefix(o, "https://") && !strings.HasPrefix(o, "http://") {
			return nil, fmt.Errorf("permissions.network[%d] 不是 origin: %q", i, o)
		}
		m.Permissions.Network[i] = o
	}
	if len(m.Permissions.Network) > 8 {
		return nil, fmt.Errorf("permissions.network 最多 8 个 origin")
	}
	return &m, nil
}

// safeRel 拒绝绝对路径、反斜杠与 .. 穿越。
func safeRel(p string) error {
	if p == "" {
		return fmt.Errorf("路径为空")
	}
	if strings.Contains(p, `\`) || strings.Contains(p, "..") || filepath.IsAbs(p) {
		return fmt.Errorf("非法路径: %q", p)
	}
	return nil
}
