// Package qwencdp 千问网页版凭据的浏览器通道。
// 用系统 Edge/Chrome 打开一个专用配置目录并开放 CDP 调试端口：
// headed 模式让用户在窗口里正常登录 chat.qwen.ai，headless 模式在后台
// 通过 DevTools 协议读取最新登录 cookie——免去手动 F12 抓包（参考 qwengate
// 的浏览器配置文件方案：cookie 长期留在配置目录，可反复读取）。
package qwencdp

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Cookie CDP 返回的 cookie 子集。
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

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

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// FreePort 取一个空闲本地端口。
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Launch 启动浏览器（headed 登录 / headless 取 cookie），返回已启动进程。
func Launch(exe, profileDir string, port int, url string, headless bool) (*exec.Cmd, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	args := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=" + fmt.Sprintf("%d", port),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,msSmartScreenPrompt",
		"--hide-crash-restore-bubble",
	}
	if headless {
		args = append(args, "--headless=new", "--window-size=800,600")
	}
	args = append(args, url)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// WaitDebug 等待调试端口就绪（/json/version 可访问）。
func WaitDebug(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		client := &http.Client{Timeout: 2 * time.Second}
		if resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port)); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// GetCookies 通过 CDP（浏览器级 WebSocket）读取全部 cookie。
func GetCookies(port int) ([]Cookie, error) {
	var ver struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&ver); err != nil {
		return nil, err
	}
	if ver.WebSocketDebuggerURL == "" {
		return nil, errors.New("调试接口未返回 webSocketDebuggerUrl")
	}
	ws, err := wsDial(ver.WebSocketDebuggerURL)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	raw, err := ws.Call(1, "Storage.getCookies", map[string]any{}, 15*time.Second)
	if err != nil {
		// 旧内核兜底 Network.getAllCookies
		raw, err = ws.Call(2, "Network.getAllCookies", map[string]any{}, 15*time.Second)
		if err != nil {
			return nil, err
		}
	}
	var out struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Cookies, nil
}

// PickAuth 从 cookie 里挑登录凭据。
// 新版千问登录态不再落 token cookie：网页用 refresh_token 换短期 access token
// 放内存/localStorage。所以：
//   - refresh：名字含 "refresh"（refresh_token）
//   - token：仅认名字恰为 "token" 的旧式 cookie；auth-session 是 auth.qwen.ai
//     的 OAuth 会话，不是 API 令牌，绝不能当 token（会污染账号）
// token 通常为空属正常，调用方应拿 refresh 去换（qwen.ExchangeRefresh）。
func PickAuth(cookies []Cookie) (token, refresh string) {
	for _, c := range cookies {
		if !strings.Contains(strings.ToLower(c.Domain), "qwen.ai") {
			continue
		}
		n := strings.ToLower(c.Name)
		if strings.Contains(n, "refresh") {
			if refresh == "" {
				refresh = c.Value
			}
			continue
		}
		if n == "token" && token == "" {
			token = c.Value
		}
	}
	return token, refresh
}

// CookieHeader 按 RFC 6265 域匹配规则把 cookie 拼成请求头值：
// host 与 cookie Domain 后缀匹配（.qwen.ai 匹配所有子域；auth.qwen.ai
// 的 cookie 不会发给 chat.qwen.ai），顺序保持 CDP 返回序。
// 用途：把真实浏览器的全套风控 cookie（cna/tfstk/isg…）带上，降低 WAF 风控评分。
func CookieHeader(cookies []Cookie, host string) string {
	var parts []string
	for _, c := range cookies {
		d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if d == "" || c.Name == "" {
			continue
		}
		if host != d && !strings.HasSuffix(host, "."+d) {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// ProfileHasCookies 配置目录是否已有 cookie 数据。
// 新版 Chromium/Edge 把 Cookies 移到了 Default/Network/ 下，两个位置都认。
func ProfileHasCookies(profileDir string) bool {
	for _, rel := range []string{filepath.Join("Default", "Network", "Cookies"), filepath.Join("Default", "Cookies")} {
		if _, err := os.Stat(filepath.Join(profileDir, rel)); err == nil {
			return true
		}
	}
	return false
}

// ── 极简 WebSocket 客户端（仅够 CDP 调用：文本帧 + ping/pong + 分片） ──

type wsConn struct {
	c  net.Conn
	br *bufio.Reader
}

func wsDial(raw string) (*wsConn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		conn.Close()
		return nil, err
	}
	req := "GET " + u.RequestURI() + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		conn.Close()
		return nil, fmt.Errorf("ws 握手失败: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &wsConn{c: conn, br: br}, nil
}

// Close 断开 WebSocket。
func (w *wsConn) Close() error { return w.c.Close() }

// Call 发送一条 CDP 命令并等对应 id 的响应。
func (w *wsConn) Call(id int, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	payload, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := w.writeFrame(0x1, payload); err != nil {
		return nil, err
	}
	w.c.SetReadDeadline(time.Now().Add(timeout))
	defer w.c.SetReadDeadline(time.Time{})
	for {
		data, err := w.readMessage()
		if err != nil {
			return nil, err
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.ID != nil && *msg.ID == id {
			if msg.Error != nil {
				return nil, fmt.Errorf("CDP %s: %s", method, msg.Error.Message)
			}
			return msg.Result, nil
		}
		// 事件帧忽略
	}
}

func (w *wsConn) writeFrame(op byte, b []byte) error {
	hdr := []byte{0x80 | op}
	n := len(b)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i, v := range b {
		masked[i] = v ^ mask[i%4]
	}
	w.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := w.c.Write(append(hdr, masked...))
	return err
}

func (w *wsConn) readMessage() ([]byte, error) {
	var out []byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(w.br, h); err != nil {
			return nil, err
		}
		fin := h[0]&0x80 != 0
		op := h[0] & 0x0f
		ln := int(h[1] & 0x7f)
		if h[1]&0x80 != 0 {
			return nil, errors.New("ws: 服务器帧不应带掩码")
		}
		switch ln {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(w.br, ext); err != nil {
				return nil, err
			}
			ln = int(ext[0])<<8 | int(ext[1])
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(w.br, ext); err != nil {
				return nil, err
			}
			ln = 0
			for _, b := range ext {
				ln = ln<<8 | int(b)
			}
		}
		payload := make([]byte, ln)
		if _, err := io.ReadFull(w.br, payload); err != nil {
			return nil, err
		}
		switch op {
		case 0x9: // ping → pong，继续读
			if err := w.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0x8:
			return nil, errors.New("ws 已被对端关闭")
		case 0x0: // 分片续帧
			out = append(out, payload...)
		default: // 文本/二进制首帧
			out = payload
		}
		if fin {
			return out, nil
		}
	}
}
