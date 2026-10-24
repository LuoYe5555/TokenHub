// Package accel 网络加速（借鉴 fastgit 的"测速选最快"与 dev-sidecar 的
// "本地代理 + DNS 优选 + 自动系统代理"思路，取其不装证书、不做 MITM 的
// 安全子集）：
//
//  1. 本地 HTTP 代理（127.0.0.1:固定端口）——处理浏览器发来的 CONNECT
//     隧道与明文 HTTP 转发，只做 TCP 中继，绝不解密 TLS。
//  2. DNS 优选 —— 对加速名单里的域名（WorkBuddy 国际站、GitHub 等），
//     并发问系统 DNS + 阿里 DoH + 腾讯 DoH 拿到候选 IP，再对所有候选
//     同时发起 TCP 连接竞速，谁先握手成功用谁（fastgit 镜像测速思路）。
//  3. 结果缓存 10 分钟；缓存 IP 连不上自动失效重选。
//  4. 一键系统代理 —— 开启时备份注册表里原有代理设置（含 PAC），
//     关闭/退出时还原；与 Clash 等代理软件互斥（开启前会覆盖系统代理）。
//
// 边界：只监听 127.0.0.1，只做中继与选路，不缓存内容、不改写报文、
// 不安装任何证书。
package accel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/logx"
)

const (
	ListenAddr = "127.0.0.1:18687"       // 本地加速代理监听地址
	cacheTTL   = 10 * time.Minute        // 优选 IP 缓存时长
	lookupWait = 1500 * time.Millisecond // DNS 收集窗口
	dialWait   = 3 * time.Second         // 单个候选 IP 的 TCP 握手超时
	maxCands   = 8                       // 最多同时竞速的 IP 数
)

// accelDomains 加速名单（后缀匹配，含子域）。聚焦 TokenHub 需要打开的站点。
var accelDomains = []string{
	"workbuddy.ai", // WorkBuddy 国际站（官网/登录/CDN）
	"codebuddy.ai", // 国际版静态资源（此前白屏的 JS 源）
	"github.com",   // 参考仓库 / release 下载
	"githubusercontent.com",
	"githubassets.com",
	"github.io",
	"jsdelivr.net", // 前端 CDN
	"hf.co",        // HuggingFace
	"huggingface.co",
}

// isAccelDomain 判断域名是否在加速名单（后缀匹配）。
func isAccelDomain(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range accelDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

type cacheEntry struct {
	ip  string
	exp time.Time
}

// pacURL 系统代理 PAC 脚本地址（由本代理自己服务）。
const pacURL = "http://" + ListenAddr + "/pac"

// proxyBackup 开启加速前备份的用户原系统代理设置（内存 + data/accel_proxy_backup.json）。
type proxyBackup struct {
	ProxyEnable   uint32 `json:"proxyEnable"`
	ProxyServer   string `json:"proxyServer"`
	ProxyOverride string `json:"proxyOverride"`
	AutoConfigURL string `json:"autoConfigURL"`       // 用户原有的 PAC 地址（Watt Toolkit 等设置的）
	PacScript     string `json:"pacScript,omitempty"` // 原 PAC 脚本内容（尽力抓取，用于串联）
}

// isHTTPURL 能否通过 HTTP 抓取该 PAC 地址（file:// 等抓不了）。
func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// fetchPAC 抓取原 PAC 脚本内容（最多 1MB），失败返回空串。
func fetchPAC(rawurl string) string {
	cli := &http.Client{Timeout: 3 * time.Second}
	resp, err := cli.Get(rawurl)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	return string(b)
}

// Manager 网络加速管理器：本地代理 + 系统代理开关。
type Manager struct {
	cfg *config.Config

	mu       sync.Mutex
	ln       net.Listener
	srv      *http.Server
	running  bool
	sysProxy bool         // 系统代理当前是否由我们接管（sysproxy_*.go 维护）
	backup   *proxyBackup // 开启前的用户原代理设置（PAC 串联要用）

	cache sync.Map // 域名(小写) -> *cacheEntry

	fwd *http.Transport // 明文 HTTP 转发用（HTTPS 走 CONNECT 隧道）
}

// New 创建管理器（cfg 用于 DataDir 备份路径与开关持久化）。
func New(cfg *config.Config) *Manager {
	m := &Manager{cfg: cfg}
	m.fwd = &http.Transport{
		DialContext:        m.dialFastContext,
		MaxIdleConns:       64,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: false,
	}
	return m
}

// Start 启动本地加速代理。已在运行则幂等返回 nil。
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}
	ln, err := net.Listen("tcp", ListenAddr)
	if err != nil {
		return err
	}
	m.ln = ln
	m.srv = &http.Server{Handler: m, ReadHeaderTimeout: 15 * time.Second}
	m.running = true
	go func() {
		if err := m.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logx.Errorf("accel", "加速代理异常退出: %v", err)
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}
	}()
	logx.Infof("accel", "网络加速代理已启动: http://%s", ListenAddr)
	return nil
}

// Stop 停止本地代理（不动系统代理设置，还原由 DisableSystemProxy 负责）。
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	if m.srv != nil {
		_ = m.srv.Close()
	}
	if m.ln != nil {
		_ = m.ln.Close()
	}
	logx.Infof("accel", "网络加速代理已停止")
}

// Running 返回本地代理是否在运行。
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Status 概览信息。
func (m *Manager) Status() map[string]any {
	n := 0
	m.cache.Range(func(_, _ any) bool { n++; return true })
	return map[string]any{
		"running":  m.Running(),
		"sysProxy": m.SystemProxyOn(),
		"addr":     ListenAddr,
		"cached":   n,
		"domains":  len(accelDomains),
	}
}

// ── 代理入口 ──

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// PAC 脚本（系统代理设置指向这里；浏览器直接抓取，不走代理语义）
	if r.URL.Host == "" && (r.URL.Path == "/pac" || r.URL.Path == "/pac/") {
		w.Header().Set("Content-Type", "application/x-ns-proxy-auto-config")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, m.pacScript())
		return
	}
	if r.Method == http.MethodConnect {
		m.handleConnect(w, r)
		return
	}
	m.handleHTTP(w, r)
}

// pacScript 生成 PAC：加速名单域名 → 走本地加速代理；其余流量按用户
// 原设置串联（原全局代理 / 原 PAC 脚本），没有任何原设置时直连。
// 效果：国内网站与未列域名完全不经我们，行为与不开加速一模一样。
func (m *Manager) pacScript() string {
	m.mu.Lock()
	b := m.backup
	m.mu.Unlock()
	fallback := "DIRECT"
	var orig string
	if b != nil {
		if b.ProxyServer != "" {
			fallback = "PROXY " + b.ProxyServer // 原全局代理（Clash 等）继续接管其余流量
		}
		orig = b.PacScript
	}
	var sb strings.Builder
	sb.WriteString("// TokenHub 网络加速 PAC（自动生成，勿手改）\n")
	sb.WriteString("var __wbDomains=[")
	for i, d := range accelDomains {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(strconv.Quote(d))
	}
	sb.WriteString("];\n")
	if orig != "" {
		// 原有 PAC 原文内联进闭包，未命中名单的流量按它的规则走（Watt Toolkit PAC 模式等）
		sb.WriteString("var __wbOrig=null;\ntry{\n__wbOrig=(function(){\n")
		sb.WriteString(orig)
		sb.WriteString("\ntry{return FindProxyForURL}catch(e){return null}\n})();\n}catch(e){__wbOrig=null}\n")
	} else {
		sb.WriteString("var __wbOrig=null;\n")
	}
	sb.WriteString("function FindProxyForURL(url, host) {\n")
	sb.WriteString("  host = String(host).toLowerCase();\n")
	sb.WriteString("  for (var i = 0; i < __wbDomains.length; i++) {\n")
	sb.WriteString("    var d = __wbDomains[i];\n")
	sb.WriteString("    if (host === d) return 'PROXY " + ListenAddr + "';\n")
	sb.WriteString("    if (host.length > d.length && host.lastIndexOf('.' + d) === host.length - d.length - 1) return 'PROXY " + ListenAddr + "';\n")
	sb.WriteString("  }\n")
	sb.WriteString("  if (__wbOrig) { try { var r = __wbOrig(url, host); if (r) return r; } catch (e) {} }\n")
	sb.WriteString("  return '" + fallback + "';\n")
	sb.WriteString("}\n")
	return sb.String()
}

// handleConnect HTTPS 隧道：TCP 中继，不碰内容。
func (m *Manager) handleConnect(w http.ResponseWriter, r *http.Request) {
	dst := r.Host
	if dst == "" {
		http.Error(w, "bad CONNECT", 400)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", 500)
		return
	}
	server, err := m.dialFast(r.Context(), dst)
	if err != nil {
		logx.Infof("accel", "CONNECT %s 失败: %v", dst, err)
		http.Error(w, "dial failed", 502)
		return
	}
	defer server.Close()
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, buf); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
	_ = buf.Flush()
}

// handleHTTP 明文 HTTP 转发（浏览器对 https 一律走 CONNECT，这里很少触发）。
func (m *Manager) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host == "" {
		http.Error(w, "accel proxy: 请把浏览器代理指向 "+ListenAddr, 400)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	resp, err := m.fwd.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ── DNS 优选 + 连接竞速 ──

// dialFastContext 给 http.Transport 用（URL 里 host 可能不带端口）。
func (m *Manager) dialFastContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return m.dialFast(ctx, addr)
}

// dialFast 对加速名单域名做优选竞速；其余域名普通直连。
func (m *Manager) dialFast(ctx context.Context, hostPort string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		host, port = hostPort, "443"
	}
	if !isAccelDomain(host) {
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	}
	key := strings.ToLower(strings.TrimSuffix(host, "."))

	// 1) 缓存命中：直接连缓存 IP，连不上再重选
	if e, ok := m.cache.Load(key); ok {
		ce := e.(*cacheEntry)
		if time.Now().Before(ce.exp) {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(ce.ip, port), dialWait)
			if err == nil {
				return c, nil
			}
			m.cache.Delete(key) // 缓存失效
		} else {
			m.cache.Delete(key)
		}
	}
	// 2) 优选竞速
	ip, err := m.pickIP(ctx, key, port)
	if err != nil {
		// 兜底：普通解析直连，绝不让加速功能把请求弄挂
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), dialWait)
	if err != nil {
		m.cache.Delete(key)
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	}
	m.cache.Store(key, &cacheEntry{ip: ip, exp: time.Now().Add(cacheTTL)})
	return c, nil
}

// pickIP 并发收集候选 IP（系统 DNS + 双 DoH），再全部竞速 TCP 握手取最快。
func (m *Manager) pickIP(ctx context.Context, host, port string) (string, error) {
	lctx, cancel := context.WithTimeout(ctx, 3*lookupWait)
	defer cancel()
	res := make(chan string, 32)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // 系统 DNS
		defer wg.Done()
		if ips, err := net.LookupHost(host); err == nil {
			for _, ip := range ips {
				res <- ip
			}
		}
	}()
	go func() { // 阿里 DoH（IP 直连，不依赖 DNS）
		defer wg.Done()
		for _, ip := range dohJSON(lctx, "https://223.5.5.5/resolve", host) {
			res <- ip
		}
	}()
	go func() { // 腾讯 DoH
		defer wg.Done()
		for _, ip := range dohJSON(lctx, "https://doh.pub/dns-query", host) {
			res <- ip
		}
	}()
	go func() { wg.Wait(); close(res) }()

	seen := map[string]bool{}
	var cands []string
	timeout := time.After(lookupWait)
collect:
	for {
		select {
		case ip, ok := <-res:
			if !ok {
				break collect
			}
			if p := net.ParseIP(ip); p != nil && p.To4() != nil && !seen[ip] {
				seen[ip] = true
				cands = append(cands, ip)
			}
		case <-timeout:
			break collect
		case <-lctx.Done():
			break collect
		}
	}
	if len(cands) == 0 {
		return "", errors.New("无候选 IP")
	}
	if len(cands) > maxCands {
		cands = cands[:maxCands]
	}
	type result struct {
		ip string
		c  net.Conn
	}
	out := make(chan result, len(cands))
	for _, ip := range cands {
		go func(ip string) {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), dialWait)
			if err != nil {
				out <- result{}
				return
			}
			out <- result{ip: ip, c: c}
		}(ip)
	}
	var winIP string
	var winConn net.Conn
	for i := 0; i < len(cands); i++ {
		r := <-out
		if r.c != nil {
			if winConn == nil {
				winIP, winConn = r.ip, r.c
			} else {
				_ = r.c.Close() // 输家的连接立即关掉
			}
		}
	}
	if winConn == nil {
		return "", errors.New("所有候选 IP 握手失败")
	}
	_ = winConn.Close() // 竞速连接只是探路，真正连接由调用方重新拨（复用走缓存）
	logx.Infof("accel", "DNS 优选 %s -> %s（候选 %d 个，缓存 %s）", host, winIP, len(cands), cacheTTL)
	return winIP, nil
}

// dohJSON 查询 DoH 的 JSON 接口（RFC 8484 JSON 变体），返回 A 记录 IPv4 列表。
func dohJSON(ctx context.Context, endpoint, host string) []string {
	cli := &http.Client{Timeout: 2 * time.Second}
	u := endpoint + "?name=" + url.QueryEscape(host) + "&type=A"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := cli.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out) != nil {
		return nil
	}
	var ips []string
	for _, a := range out.Answer {
		if a.Type == 1 && net.ParseIP(a.Data) != nil {
			ips = append(ips, a.Data)
		}
	}
	return ips
}

// portOf 从 authority 里拆端口（默认 443）。
func portOf(hostPort string) string {
	if _, port, err := net.SplitHostPort(hostPort); err == nil {
		if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
			return port
		}
	}
	return "443"
}
