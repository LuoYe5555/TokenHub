// Package updater 实现 TokenHub 自更新：比对 GitHub latest release 的
// tag（release-<commit短哈希>）与本地构建时注入的 GitCommit，不一致即
// 下载新 TokenHub.exe 替换并自动重启。
//
// 下载链：GitHub 直连优先，失败按序尝试国内镜像前缀。
// Windows 下运行中的 exe 不能覆盖但可以重命名：当前 exe → TokenHub.prev.exe，
// 新文件 → TokenHub.exe，然后 spawn 新进程退出旧进程。
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/logx"
)

const repoAPI = "https://api.github.com/repos/LuoYe5555/TokenHub/releases/latest"

// mirrors GitHub 直连失败时按序尝试的镜像前缀（国内网络环境）。
var mirrors = []string{"https://gh-proxy.com/", "https://ghfast.top/", "https://ghproxy.net/"}

var tagRe = regexp.MustCompile(`([0-9a-fA-F]{7,40})$`)

// Status 更新器状态（面板展示用）。
type Status struct {
	Current   string `json:"current"`   // 本地构建 commit
	Latest    string `json:"latest"`    // 最新 release tag
	CheckedAt int64  `json:"checkedAt"` // 最近一次检查时间
	LastState string `json:"lastState"` // up_to_date / updated / failed / pending_restart / ""
	LastMsg   string `json:"lastMsg"`
}

type Manager struct {
	cfg *config.Config

	mu      sync.Mutex
	busy    bool // CheckNow 进行中（防循环检查与手动触发并发）
	st      Status
	exePath string
}

func New(cfg *config.Config) *Manager {
	exe, _ := os.Executable()
	return &Manager{cfg: cfg, exePath: exe}
}

// Start 启动后台检查循环：启动 90 秒后首查，之后每 6 小时一次。
func (m *Manager) Start() {
	if !m.cfg.AutoUpdate {
		logx.Infof("updater", "自动更新已关闭")
		return
	}
	if runtime.GOOS != "windows" {
		logx.Infof("updater", "非 Windows 平台不支持自动更新，请手动更新")
		return
	}
	go func() {
		time.Sleep(90 * time.Second)
		m.CheckNow(true)
		for range time.Tick(6 * time.Hour) {
			if !m.cfg.AutoUpdate {
				return
			}
			m.CheckNow(true)
		}
	}()
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// CheckNow 检查并（apply=true 时）应用更新。返回状态与说明。
func (m *Manager) CheckNow(apply bool) Status {
	if runtime.GOOS != "windows" {
		m.setState("up_to_date", "非 Windows 平台不支持自动更新，请重新下载 TokenHub-linux-amd64")
		return m.Status()
	}
	m.mu.Lock()
	if m.busy {
		st := m.st
		m.mu.Unlock()
		return st
	}
	m.busy = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.busy = false; m.mu.Unlock() }()

	tag, assetURL, err := m.latest()
	m.mu.Lock()
	if tag != "" {
		m.st.Latest = tag
		m.st.CheckedAt = time.Now().Unix()
	}
	m.mu.Unlock()
	if err != nil {
		m.setState("failed", "检查更新失败: "+err.Error())
		return m.Status()
	}
	local := normalizedCommit()
	if local == "dev" {
		m.setState("up_to_date", "本地为开发构建（未注入版本），跳过自动更新")
		return m.Status()
	}
	if tag == "" || local == tagCommit(tag) {
		m.setState("up_to_date", "已是最新版本 "+config.AppVersion)
		return m.Status()
	}
	if !apply {
		m.setState("pending_restart", "发现新版本 "+tag+"，当前 "+local)
		return m.Status()
	}
	if err := m.downloadAndSwap(assetURL); err != nil {
		m.setState("failed", "下载更新失败: "+err.Error())
		return m.Status()
	}
	m.setState("pending_restart", "已更新到 "+tag+"，正在重启…")
	logx.Infof("updater", "自动更新: %s → %s，准备重启", local, tag)
	m.restart()
	return m.Status()
}

func (m *Manager) setState(state, msg string) {
	m.mu.Lock()
	m.st.LastState = state
	m.st.LastMsg = msg
	m.mu.Unlock()
	logx.Infof("updater", "%s (%s)", state, msg)
}

// latest 查询最新 release，返回 tag 与 TokenHub.exe 资产下载地址。
func (m *Manager) latest() (tag, assetURL string, err error) {
	ctx := context.Background()
	var body []byte
	for attempt, u := range append([]string{repoAPI}, mirrorURLs(repoAPI)...) {
		resp, e := httpx.Do(ctx, "GET", u, map[string]string{"User-Agent": config.AppName}, nil, 25*time.Second)
		if e != nil || resp.Status != 200 {
			continue
		}
		body = resp.Body
		_ = attempt
		break
	}
	if len(body) == 0 {
		return "", "", fmt.Errorf("所有渠道均不可达（直连+镜像）")
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if json.Unmarshal(body, &rel) != nil || rel.TagName == "" {
		return "", "", fmt.Errorf("响应解析失败")
	}
	for _, a := range rel.Assets {
		if a.Name == "TokenHub.exe" {
			return rel.TagName, a.URL, nil
		}
	}
	return "", "", fmt.Errorf("release %s 缺少 TokenHub.exe 资产", rel.TagName)
}

// downloadAndSwap 下载新 exe（>5MB 且 PE 头校验）→ 替换当前 exe。
func (m *Manager) downloadAndSwap(assetURL string) error {
	if assetURL == "" {
		return fmt.Errorf("下载地址为空")
	}
	ctx := context.Background()
	var bin []byte
	for _, u := range append([]string{assetURL}, mirrorURLs(assetURL)...) {
		resp, e := httpx.Do(ctx, "GET", u, map[string]string{"User-Agent": config.AppName}, nil, 10*time.Minute)
		if e != nil || resp.Status != 200 {
			continue
		}
		bin = resp.Body
		break
	}
	if len(bin) < 5<<20 {
		return fmt.Errorf("下载内容过小（%d 字节），疑似失败", len(bin))
	}
	if len(bin) < 2 || bin[0] != 'M' || bin[1] != 'Z' {
		return fmt.Errorf("下载内容不是有效的 Windows 可执行文件")
	}
	dir := filepath.Dir(m.exePath)
	tmp := filepath.Join(dir, "TokenHub.update.exe")
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	// 旧备份先清掉（可能在则删，不在则忽略）
	_ = os.Remove(filepath.Join(dir, "TokenHub.prev.exe"))
	// 运行中的 exe 允许重命名：当前 → prev，update → 正式
	prev := filepath.Join(dir, "TokenHub.prev.exe")
	if err := os.Rename(m.exePath, prev); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("重命名当前 exe 失败: %w", err)
	}
	if err := os.Rename(tmp, m.exePath); err != nil {
		// 回滚：把旧 exe 改回原名
		_ = os.Rename(prev, m.exePath)
		return fmt.Errorf("就位新 exe 失败: %w", err)
	}
	return nil
}

// restart 延迟 2 秒后 spawn 新 exe，然后退出当前进程（给在途响应留收尾时间）。
func (m *Manager) restart() {
	if runtime.GOOS != "windows" {
		return
	}
	go func() {
		time.Sleep(2 * time.Second)
		p, err := os.StartProcess(m.exePath, []string{m.exePath}, &os.ProcAttr{
			Dir: filepath.Dir(m.exePath),
			Env: os.Environ(),
			Files: []*os.File{
				os.Stdin, os.Stdout, os.Stderr,
			},
		})
		if err != nil {
			m.setState("failed", "自动重启失败: "+err.Error()+"，请手动启动")
			return
		}
		_ = p.Release()
		logx.Infof("updater", "已拉起新版本进程，退出当前实例")
		os.Exit(0)
	}()
}

// tagCommit 从 release tag 里取结尾的 commit 短哈希。兼容两种格式：
// release-3c4d33f 与 CI 生成的 <commit说明>-3c4d33f（见 .github/workflows/release.yml）。
func tagCommit(tag string) string {
	if mm := tagRe.FindStringSubmatch(tag); mm != nil {
		return mm[1]
	}
	return tag
}

// normalizedCommit 本地构建 commit（ldflags 注入，小写）。
func normalizedCommit() string {
	c := config.GitCommit
	if c == "" {
		return "dev"
	}
	if len(c) >= 7 {
		c = c[:7]
	}
	for i, b := range []byte(c) {
		if b >= 'A' && b <= 'F' {
			c = c[:i] + string(b+32) + c[i+1:]
		}
	}
	return c
}

// mirrorURLs 为直连地址生成镜像前缀候选。
func mirrorURLs(u string) []string {
	out := make([]string, 0, len(mirrors))
	for _, m := range mirrors {
		out = append(out, m+u)
	}
	return out
}
