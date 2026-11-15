package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/logx"
)

// Installed 注册表条目。
type Installed struct {
	ID          string `json:"id"`
	Enabled     bool   `json:"enabled"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	Source      string `json:"source"` // local | github
	SourceRef   string `json:"sourceRef,omitempty"`
	InstalledAt string `json:"installedAt"`
}

// View 面板展示条目。
type View struct {
	Installed
	Description string `json:"description"`
	Author      string `json:"author"`
	HasPage     bool   `json:"hasPage"`
	PageURL     string `json:"pageURL,omitempty"`
	PageTitle   string `json:"pageTitle,omitempty"`
}

// Manager 插件管理器。
type Manager struct {
	root string // DataDir/plugins
	mu   sync.Mutex
	reg  map[string]*Installed
	rt   map[string]*Runtime
	// DataFn 面板注入的数据面（th.data.models() 调它），可为 nil。
	DataFn func() any
}

// NewManager 创建并加载注册表，启动所有 enabled 插件。
func NewManager(dataDir string) (*Manager, error) {
	m := &Manager{
		root: filepath.Join(dataDir, "plugins"),
		reg:  map[string]*Installed{},
		rt:   map[string]*Runtime{},
	}
	for _, d := range []string{m.root, filepath.Join(m.root, "installed"), filepath.Join(m.root, "data")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	m.loadRegistry()
	m.startAll()
	return m, nil
}

func (m *Manager) regPath() string   { return filepath.Join(m.root, "installed.json") }
func (m *Manager) dir(id string) string { return filepath.Join(m.root, "installed", id) }

// InstalledDir 插件安装目录（面板静态资源服务用）。
func (m *Manager) InstalledDir(id string) string { return m.dir(id) }

func (m *Manager) loadRegistry() {
	raw, err := os.ReadFile(m.regPath())
	if err != nil {
		return
	}
	var list []*Installed
	if json.Unmarshal(raw, &list) != nil {
		logx.Warnf("plugins", "installed.json 损坏，忽略")
		return
	}
	for _, it := range list {
		m.reg[it.ID] = it
	}
}

func (m *Manager) saveRegistry() {
	list := make([]*Installed, 0, len(m.reg))
	for _, it := range m.reg {
		list = append(list, it)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	raw, _ := json.MarshalIndent(list, "", "  ")
	_ = os.WriteFile(m.regPath(), raw, 0o600)
}

func (m *Manager) audit(action, id, detail string) {
	e := map[string]any{"time": time.Now().Format(time.RFC3339), "action": action, "id": id, "detail": detail}
	raw, _ := json.Marshal(e)
	f, err := os.OpenFile(filepath.Join(m.root, "audit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(raw, '\n'))
		_ = f.Close()
	}
	logx.Infof("plugins", "%s: %s (%s)", action, id, detail)
}

// List 面板列表（按 id 排序）。
func (m *Manager) List() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.reg))
	for id := range m.reg {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]View, 0, len(ids))
	for _, id := range ids {
		it := m.reg[id]
		v := View{Installed: *it}
		if man, err := LoadManifest(m.dir(id)); err == nil {
			v.Description = man.Description
			v.Author = man.Author
			if man.UI.Entry != "" {
				v.HasPage = true
				v.PageURL = "/plugins-assets/" + id + "/" + strings.TrimPrefix(man.UI.Entry, "ui/")
				v.PageTitle = man.UI.Title
				if v.PageTitle == "" {
					v.PageTitle = man.Name
				}
			}
		}
		out = append(out, v)
	}
	return out
}

// InstallDir 从本地目录安装（复制文件，拒绝符号链接）。
func (m *Manager) InstallDir(src string, sourceRef string) (*Manifest, error) {
	src = filepath.Clean(src)
	man, err := LoadManifest(src)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dst := m.dir(man.ID)
	// 先装到临时目录再原子替换
	tmp := dst + ".tmp"
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(dst)
	n, err := copyTree(src, tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	old := m.reg[man.ID]
	enabled := old == nil || old.Enabled // 新装默认启用；重装保持原状态
	m.reg[man.ID] = &Installed{
		ID: man.ID, Enabled: enabled,
		Version: man.Version, Name: man.Name,
		Source: sourceOf(sourceRef), SourceRef: sourceRef,
		InstalledAt: time.Now().Format(time.RFC3339),
	}
	m.saveRegistry()
	m.audit("install", man.ID, fmt.Sprintf("v%s source=%s files=%d", man.Version, sourceOf(sourceRef), n))
	if enabled {
		if err := m.startLocked(man.ID); err != nil {
			return man, err
		}
	}
	return man, nil
}

func sourceOf(ref string) string {
	if strings.Contains(ref, "github.com") || strings.HasSuffix(ref, ".zip") {
		return "github"
	}
	return "local"
}

// copyTree 递归复制目录；拒绝符号链接。
func copyTree(src, dst string) (int, error) {
	n := 0
	return n, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("拒绝符号链接: %s", path)
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("拒绝非常规文件: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		n++
		return nil
	})
}

// Toggle 启用/停用。
func (m *Manager) Toggle(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.reg[id]
	if !ok {
		return fmt.Errorf("插件未安装: %s", id)
	}
	it.Enabled = enabled
	m.saveRegistry()
	m.audit(map[bool]string{true: "enable", false: "disable"}[enabled], id, it.Version)
	if enabled {
		return m.startLocked(id)
	}
	if rt := m.rt[id]; rt != nil {
		rt.stop()
		delete(m.rt, id)
	}
	return nil
}

// Uninstall 卸载（删目录、注册表、存储）。
func (m *Manager) Uninstall(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reg[id]; !ok {
		return fmt.Errorf("插件未安装: %s", id)
	}
	if rt := m.rt[id]; rt != nil {
		rt.stop()
		delete(m.rt, id)
	}
	delete(m.reg, id)
	m.saveRegistry()
	_ = os.RemoveAll(m.dir(id))
	_ = os.Remove(filepath.Join(m.root, "data", id+".json"))
	m.audit("uninstall", id, "")
	return nil
}

// Call 调用插件的 onRpc(method, params)。
func (m *Manager) Call(id, method string, params any) (any, error) {
	m.mu.Lock()
	rt := m.rt[id]
	m.mu.Unlock()
	if rt == nil {
		return nil, fmt.Errorf("插件未运行: %s", id)
	}
	return rt.Call(method, params)
}

// Close 停止全部插件运行时（进程退出/测试清理用）。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rt := range m.rt {
		rt.stop()
		delete(m.rt, id)
	}
}

// startAll 启动全部 enabled 插件（启动期单个失败不影响其他）。
func (m *Manager) startAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.reg))
	for id, it := range m.reg {
		if it.Enabled {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		_ = m.startLocked(id)
	}
}

// startLocked 启动单个插件（须已持锁）。
func (m *Manager) startLocked(id string) error {
	dir := m.dir(id)
	man, err := LoadManifest(dir)
	if err != nil {
		m.audit("failed", id, err.Error())
		return err
	}
	rt, err := newRuntime(m, man, dir)
	if err != nil {
		m.audit("failed", id, err.Error())
		return err
	}
	m.rt[id] = rt
	return nil
}
