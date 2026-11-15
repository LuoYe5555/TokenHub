package plugins

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tokenhub/internal/logx"
)

// Mirrors 可选镜像前缀（拼在 https://github.com/... 完整 URL 前面）。
var Mirrors = []string{"", "https://ghproxy.net/", "https://gh-proxy.com/"}

// githubURL 把 "owner/repo" 或完整仓库地址归一成仓库主页 URL。
func githubURL(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("仓库地址为空")
	}
	if !strings.Contains(repo, "github.com") {
		// 视为 owner/repo
		parts := strings.Split(strings.Trim(repo, "/"), "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("仓库地址须为 owner/repo 或完整 URL: %q", repo)
		}
		return "https://github.com/" + parts[0] + "/" + parts[1], nil
	}
	if i := strings.Index(repo, "github.com/"); i >= 0 {
		parts := strings.Split(strings.TrimPrefix(repo[i:], "github.com/"), "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("仓库地址须为 owner/repo: %q", repo)
		}
		return "https://github.com/" + parts[0] + "/" + parts[1], nil
	}
	return "", fmt.Errorf("无法解析仓库地址: %q", repo)
}

// downloadZip 下载 owner/repo 默认分支 zipball。mirror 非空时只用该镜像，
// 否则依次尝试直连与全部镜像；分支 main/master。
func downloadZip(repoURL, mirror string) ([]byte, error) {
	mirrors := Mirrors
	if mirror != "" {
		ok := false
		for _, mv := range Mirrors {
			if mv == mirror {
				mirrors = []string{mirror}
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("未知镜像: %q", mirror)
		}
	}
	var lastErr error
	for _, m := range mirrors {
		for _, branch := range []string{"main", "master"} {
			url := repoURL + "/archive/refs/heads/" + branch + ".zip"
			if m != "" {
				url = strings.TrimSuffix(m, "/") + "/" + url
			}
			raw, err := httpGet(url, 90*time.Second)
			if err != nil {
				lastErr = err
				continue
			}
			if looksLikeZip(raw) {
				return raw, nil
			}
			lastErr = fmt.Errorf("响应不是 zip（分支不存在或镜像返回错误）: %s", url)
		}
	}
	return nil, fmt.Errorf("下载失败（已尝试直连与镜像、main/master 分支）: %w", lastErr)
}

func looksLikeZip(raw []byte) bool {
	return len(raw) > 4 && raw[0] == 'P' && raw[1] == 'K'
}

func httpGet(url string, timeout time.Duration) ([]byte, error) {
	hc := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "TokenHub-plugins")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// InstallGitHub 从 GitHub 仓库下载 zipball 并安装。
// 包内 plugin.json 允许在根目录或唯一一层子目录（zipball 自带顶层目录）。
func (m *Manager) InstallGitHub(repo, mirror string) (*Manifest, error) {
	repoURL, err := githubURL(repo)
	if err != nil {
		return nil, err
	}
	logx.Infof("plugins", "开始从 GitHub 安装 %s", repoURL)
	raw, err := downloadZip(repoURL, mirror)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("zip 解析失败: %w", err)
	}
	// 解压到临时目录（zip-slip 防护）
	tmpRoot := filepath.Join(m.root, ".tmp-download")
	_ = os.RemoveAll(tmpRoot)
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpRoot)
	strip := zipballPrefix(zr)
	for _, f := range zr.File {
		rel := strings.TrimPrefix(f.Name, strip)
		if rel == "" {
			continue
		}
		if err := safeRel(filepath.ToSlash(rel)); err != nil {
			return nil, fmt.Errorf("zip 内非法路径 %q: %w", f.Name, err)
		}
		target := filepath.Join(tmpRoot, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o700)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return nil, err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 32<<20))
		rc.Close()
		out.Close()
		if err != nil {
			return nil, err
		}
	}
	// plugin.json 在根或唯一一层子目录
	cand := []string{tmpRoot}
	if entries, _ := os.ReadDir(tmpRoot); len(entries) == 1 && entries[0].IsDir() {
		cand = append(cand, filepath.Join(tmpRoot, entries[0].Name()))
	}
	for _, dir := range cand {
		if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err == nil {
			return m.InstallDir(dir, repoURL)
		}
	}
	return nil, fmt.Errorf("zip 内未找到 plugin.json")
}

// zipballPrefix 取 zipball 的公共顶层目录（形如 owner-branch-hash/）。
func zipballPrefix(zr *zip.Reader) string {
	if len(zr.File) == 0 {
		return ""
	}
	first := zr.File[0].Name
	i := strings.Index(first, "/")
	if i < 0 {
		return ""
	}
	prefix := first[:i+1]
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			return "" // 有顶层散落文件 → plugin.json 可能在根
		}
	}
	return prefix
}
