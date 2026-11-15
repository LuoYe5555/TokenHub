// 插件系统面板端：列表/货架/安装/启停/卸载/RPC/卡片页静态资源。
package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"tokenhub/internal/plugins"
)

func (p *Panel) pluginsReady() bool { return p.plugins != nil }

// handlePluginsList 已安装插件列表。
func (p *Panel) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	if !p.pluginsReady() {
		writeJSON(w, map[string]any{"plugins": []any{}})
		return
	}
	writeJSON(w, map[string]any{"plugins": p.plugins.List()})
}

// handlePluginsMarket 货架（内置示例 + 镜像列表）。
func (p *Panel) handlePluginsMarket(w http.ResponseWriter, r *http.Request) {
	if !p.pluginsReady() {
		writeJSON(w, map[string]any{"items": []any{}, "mirrors": []any{}})
		return
	}
	writeJSON(w, map[string]any{"items": p.plugins.Shelf(), "mirrors": plugins.Mirrors})
}

// handlePluginsInstallLocal 从本地目录安装。
func (p *Panel) handlePluginsInstallLocal(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path string `json:"path"` }
	if err := readBody(r, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		writeErr(w, 400, "缺少 path")
		return
	}
	if _, err := os.Stat(in.Path); err != nil {
		writeErr(w, 400, "目录不存在: "+in.Path)
		return
	}
	man, err := p.plugins.InstallDir(in.Path, in.Path)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": man.ID, "name": man.Name, "version": man.Version})
}

// handlePluginsInstallGitHub 从 GitHub 仓库 zipball 安装。
func (p *Panel) handlePluginsInstallGitHub(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo   string `json:"repo"`
		Mirror string `json:"mirror"`
	}
	if err := readBody(r, &in); err != nil || strings.TrimSpace(in.Repo) == "" {
		writeErr(w, 400, "缺少 repo")
		return
	}
	man, err := p.plugins.InstallGitHub(in.Repo, in.Mirror)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": man.ID, "name": man.Name, "version": man.Version})
}

// handlePluginsInstallBuiltin 安装内置货架插件。
func (p *Panel) handlePluginsInstallBuiltin(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string `json:"id"` }
	if err := readBody(r, &in); err != nil || in.ID == "" {
		writeErr(w, 400, "缺少 id")
		return
	}
	man, err := p.plugins.InstallBuiltin(in.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": man.ID, "name": man.Name, "version": man.Version})
}

// handlePluginsToggle 启用/停用。
func (p *Panel) handlePluginsToggle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := readBody(r, &in); err != nil || in.ID == "" {
		writeErr(w, 400, "缺少 id")
		return
	}
	if err := p.plugins.Toggle(in.ID, in.Enabled); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePluginsUninstall 卸载。
func (p *Panel) handlePluginsUninstall(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string `json:"id"` }
	if err := readBody(r, &in); err != nil || in.ID == "" {
		writeErr(w, 400, "缺少 id")
		return
	}
	if err := p.plugins.Uninstall(in.ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePluginsRPC 插件卡片页 → 插件后端 onRpc。
func (p *Panel) handlePluginsRPC(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}
	if err := readBody(r, &in); err != nil || in.Method == "" {
		writeErr(w, 400, "缺少 method")
		return
	}
	out, err := p.plugins.Call(id, in.Method, in.Params)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": out})
}

// assetExts 卡片页静态资源白名单扩展名。
var assetExts = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".woff": "font/woff", ".woff2": "font/woff2",
}

// servePluginAssets 插件卡片页：/plugins-assets/<id>/<path> → installed/<id>/ui/<path>。
func (p *Panel) servePluginAssets(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rel := r.PathValue("path")
	if strings.Contains(rel, "..") || strings.Contains(rel, "\\") {
		writeErr(w, 400, "非法路径")
		return
	}
	if !p.pluginsReady() {
		writeErr(w, 404, "插件系统未启用")
		return
	}
	// 只服务 installed/<id>/ui/ 下的文件
	root := filepath.Join(p.plugins.InstalledDir(id), "ui")
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(root)+string(os.PathSeparator)) &&
		filepath.Clean(full) != filepath.Clean(root) {
		writeErr(w, 400, "非法路径")
		return
	}
	ext := strings.ToLower(filepath.Ext(full))
	ct, ok := assetExts[ext]
	if !ok {
		writeErr(w, 403, "扩展名不在白名单")
		return
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
