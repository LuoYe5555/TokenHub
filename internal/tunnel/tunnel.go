// Package tunnel 公网分享：内置 bore 协议客户端（Go 原生实现，无需外部程序），
// 把本机「仅 API 监听」端口暴露为 bore.pub 上的公网 TCP 地址。
//
// 安全约定：
//   - 只做出站连接（连 bore.pub 公共中继），不在本机/路由器开放任何入站端口；
//   - 转发目标固定为本机 127.0.0.1 的「仅 API 监听」（只挂网关路由 + 强制密钥鉴权），
//     管理面板与账号凭据永远不会出现在公网地址上；
//   - 协议参考 ekzhang/bore v0.5.0（MIT License）客户端实现。
package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"tokenhub/internal/logx"
)

const (
	// ControlPort bore 服务端控制端口。
	ControlPort = 7835
	// DefaultHost 默认公共中继。
	DefaultHost = "bore.pub"
	// networkTimeout 连接与握手超时（与 bore 一致）。
	networkTimeout = 3 * time.Second
	// maxFrameSize JSON 帧上限（bore 为 256 字节，放宽一点防版本差异）。
	maxFrameSize = 4096
	// warmIdleTTL 预热连接的最长闲置时间。实测 bore.pub 中继会把闲置约 10 秒的
	// 控制连接静默关闭（写入不报错但 Accept 被丢弃），因此 TTL 必须远小于 10s。
	warmIdleTTL = 8 * time.Second
	// warmTarget 后台保持的预热连接数量。
	warmTarget = 2
	// maxWarm 预热连接池上限。
	maxWarm = 4
	// warmRefresh 预热池巡检周期。
	warmRefresh = 4 * time.Second
)

// State 隧道状态。
type State string

const (
	StateOff      State = "off"
	StateStarting State = "starting"
	StateUp       State = "up"
	StateError    State = "error"
)

// Manager 隧道管理器（单实例）。
type Manager struct {
	mu      sync.Mutex
	apiPort int    // 本机「仅 API 监听」端口（转发目标）
	host    string // bore 中继地址

	cancel    context.CancelFunc
	state     State
	publicURL string // http://bore.pub:PORT
	errMsg    string

	poolMu    sync.Mutex
	warm      []warmConn // 预热的控制连接（见 takeWarm）
}

type warmConn struct {
	c    net.Conn
	born time.Time
}

// New 创建管理器。apiPort 为本机仅 API 监听端口（隧道转发目标）。
func New(apiPort int) *Manager {
	return &Manager{apiPort: apiPort, host: DefaultHost, state: StateOff}
}

// Status 当前状态快照。
func (m *Manager) Status() (State, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.publicURL, m.errMsg
}

// Start 启动隧道（幂等：已在启动中/已开启则直接返回）。
// 握手成功后状态变为 up；控制连接断开后状态回到 off 并记录原因。
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.state == StateStarting || m.state == StateUp {
		m.mu.Unlock()
		return nil
	}
	m.state = StateStarting
	m.errMsg = ""
	m.publicURL = ""
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	go func() {
		wasUp := false
		go m.keepWarm(ctx) // 后台保持预热池
		_, err := m.runClient(ctx, func(p int) {
			wasUp = true
			m.mu.Lock()
			m.state = StateUp
			m.publicURL = fmt.Sprintf("http://%s:%d", m.host, p)
			m.mu.Unlock()
			logx.Infof("tunnel", "公网分享已开启: %s（仅 API，需密钥）", m.publicURL)
		})
		m.clearWarm() // 控制连接已断，预热池全部作废
		if err != nil {
			select {
			case <-ctx.Done():
				m.mu.Lock()
				m.state = StateOff
				m.mu.Unlock()
				return // 主动关闭，不算错误
			default:
			}
			m.mu.Lock()
			m.state = StateError
			m.errMsg = err.Error()
			if wasUp {
				m.publicURL = ""
			}
			m.mu.Unlock()
			logx.Errorf("tunnel", "公网分享异常: %v", err)
			return
		}
		// 正常退出（服务端关闭）→ 标记为断开
		if wasUp {
			m.mu.Lock()
			m.state = StateError
			m.errMsg = "与中继的连接已断开，可重新开启"
			m.publicURL = ""
			m.mu.Unlock()
			logx.Warnf("tunnel", "中继连接已断开")
		}
	}()
	return nil
}

// Stop 停止隧道。
func (m *Manager) Stop() {
	m.mu.Lock()
	c := m.cancel
	m.state = StateOff
	m.publicURL = ""
	m.mu.Unlock()
	m.clearWarm()
	if c != nil {
		c()
	}
	logx.Infof("tunnel", "公网分享已关闭")
}

// runClient 建立控制连接并循环处理服务端消息，直到出错或 ctx 取消。
// 握手成功时立即调用 onReady(公网端口) 上报就绪。
// 首连（含 DNS 解析）在国内可能偏慢，做最多 6 次重试（约 30 秒）。
func (m *Manager) runClient(ctx context.Context, onReady func(port int)) (int, error) {
	var remotePort int
	var err error
	for attempt := 1; attempt <= 6; attempt++ {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
		remotePort, err = m.dialAndLoop(ctx, onReady)
		if err == nil {
			return remotePort, nil
		}
		if errors.Is(err, errStopped) || errors.Is(err, context.Canceled) {
			return remotePort, err
		}
		// 已握手成功后断开（连接中断）不重试，交给用户手动重开
		if remotePort > 0 {
			return remotePort, err
		}
		logx.Warnf("tunnel", "连接中继失败（第 %d/6 次）: %v", attempt, err)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return 0, fmt.Errorf("连接 %s 反复失败: %w", m.host, err)
}

// errStopped 主动停止。
var errStopped = errors.New("stopped")

func (m *Manager) dialAndLoop(ctx context.Context, onReady func(port int)) (int, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(m.host, fmt.Sprint(ControlPort)), 6*time.Second)
	if err != nil {
		return 0, fmt.Errorf("无法连接 %s:%d（检查网络）: %v", m.host, ControlPort, err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)

	// 握手：Hello(0) → 服务端分配随机公网端口
	conn.SetDeadline(time.Now().Add(networkTimeout * 4))
	if err := writeFrame(conn, []byte(`{"Hello":0}`)); err != nil {
		return 0, fmt.Errorf("发送握手失败: %v", err)
	}
	frame, err := readFrame(br)
	if err != nil {
		return 0, fmt.Errorf("等待服务端响应失败: %v", err)
	}
	remotePort, err := parseHello(frame)
	if err != nil {
		return 0, err
	}
	conn.SetDeadline(time.Time{}) // 握手完成，解除超时

	// 握手成功，立即上报就绪，并预热控制连接（省掉首个请求的现场握手）
	if onReady != nil {
		onReady(remotePort)
	}
	m.warmUp(2)

	// 监听 ctx 取消
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// 主循环：处理服务端消息
	for {
		frame, err := readFrame(br)
		if err != nil {
			select {
			case <-ctx.Done():
				return remotePort, errStopped
			default:
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return remotePort, fmt.Errorf("与中继的连接已断开，可重新开启")
			}
			return remotePort, fmt.Errorf("连接异常: %v", err)
		}
		kind, id, msg, err := parseServerMsg(frame)
		if err != nil {
			return remotePort, err
		}
		switch kind {
		case "Heartbeat":
			// 服务端保活探测，忽略
		case "Connection":
			go m.handleConn(id)
		case "Hello":
			// 忽略重复 hello
		case "Challenge":
			return remotePort, errors.New("中继要求鉴权（bore.pub 无需鉴权，请检查地址）")
		case "Error":
			return remotePort, fmt.Errorf("中继错误: %s", msg)
		default:
			return remotePort, fmt.Errorf("未知消息: %s", kind)
		}
	}
}

// handleConn 处理一条公网转发连接：
// 从预热池取（或新建）一条控制连接发 Accept(id)，再与本地仅 API 端口双向对接。
func (m *Manager) handleConn(id string) {
	remote := m.takeWarm()
	if remote == nil {
		return
	}
	remote.SetDeadline(time.Now().Add(networkTimeout))
	if err := writeFrame(remote, []byte(fmt.Sprintf(`{"Accept":%q}`, id))); err != nil {
		// 预热连接可能已被中继静默回收 → 弃用后现场重拨一次兜底
		remote.Close()
		logx.Warnf("tunnel", "预热连接失效，重拨: %v", err)
		r2, err2 := net.DialTimeout("tcp", net.JoinHostPort(m.host, fmt.Sprint(ControlPort)), networkTimeout)
		if err2 != nil {
			logx.Warnf("tunnel", "转发连接建立失败: %v", err2)
			return
		}
		remote = r2
		remote.SetDeadline(time.Now().Add(networkTimeout))
		if err := writeFrame(remote, []byte(fmt.Sprintf(`{"Accept":%q}`, id))); err != nil {
			remote.Close()
			logx.Warnf("tunnel", "发送 Accept 失败: %v", err)
			return
		}
	}
	remote.SetDeadline(time.Time{})
	m.warmUp(1) // 用掉一条，后台补一条

	local, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(m.apiPort)), networkTimeout)
	if err != nil {
		remote.Close()
		logx.Warnf("tunnel", "连接本地 API 端口失败: %v", err)
		return
	}
	defer local.Close()

	pipe(local, remote)
	logx.Infof("tunnel", "公网请求已转发完成")
}

// takeWarm 取一条预热控制连接。
// 先剔除过期连接，再做活性探测：中继半开关闭的连接写入不报错但永远无响应，
// 用 1ms 超时读探测——读到 EOF 说明对端已关闭，读到超时说明连接存活。
func (m *Manager) takeWarm() net.Conn {
	for {
		m.poolMu.Lock()
		now := time.Now()
		keep := m.warm[:0]
		for _, w := range m.warm {
			if now.Sub(w.born) < warmIdleTTL {
				keep = append(keep, w)
			} else {
				w.c.Close() // 闲置过久，可能已被中继回收
			}
		}
		m.warm = keep
		var item warmConn
		have := len(m.warm) > 0
		if have {
			item = m.warm[len(m.warm)-1]
			m.warm = m.warm[:len(m.warm)-1]
		}
		m.poolMu.Unlock()
		if !have {
			break
		}
		if connAlive(item.c) {
			return item.c
		}
		item.c.Close() // 已被中继关闭，取下一条
	}
	remote, err := net.DialTimeout("tcp", net.JoinHostPort(m.host, fmt.Sprint(ControlPort)), networkTimeout)
	if err != nil {
		logx.Warnf("tunnel", "转发连接建立失败: %v", err)
		return nil
	}
	return remote
}

// connAlive 活性探测：1ms 读超时=存活（无数据可读）；EOF/错误=对端已关闭。
func connAlive(c net.Conn) bool {
	one := make([]byte, 1)
	_ = c.SetReadDeadline(time.Now().Add(1 * time.Millisecond))
	_, err := c.Read(one)
	_ = c.SetReadDeadline(time.Time{})
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true // 没数据且没断 = 活的
	}
	return err == nil // 读到数据也算活（极小概率中继提前发了字节）
}

// warmUp 后台预拨 n 条控制连接放进池（不阻塞请求路径）。
func (m *Manager) warmUp(n int) {
	go func() {
		for i := 0; i < n; i++ {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(m.host, fmt.Sprint(ControlPort)), networkTimeout)
			if err != nil {
				return
			}
			m.poolMu.Lock()
			if len(m.warm) >= maxWarm {
				m.poolMu.Unlock()
				c.Close()
				return
			}
			m.warm = append(m.warm, warmConn{c: c, born: time.Now()})
			m.poolMu.Unlock()
		}
	}()
}

// keepWarm 预热池巡检：丢弃过期/死亡连接，补足到 warmTarget 条。
// 由 Start 的 goroutine 持有，ctx 取消后退出。
func (m *Manager) keepWarm(ctx context.Context) {
	t := time.NewTicker(warmRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// 清理过期与死连接
			m.poolMu.Lock()
			live := m.warm[:0]
			now := time.Now()
			for _, item := range m.warm {
				if now.Sub(item.born) >= warmIdleTTL {
					item.c.Close()
					continue
				}
				live = append(live, item)
			}
			m.warm = live
			need := warmTarget - len(m.warm)
			m.poolMu.Unlock()
			if need > 0 {
				m.warmUp(need)
			}
		}
	}
}

// clearWarm 隧道停止时清空预热池。
func (m *Manager) clearWarm() {
	m.poolMu.Lock()
	for _, item := range m.warm {
		item.c.Close()
	}
	m.warm = nil
	m.poolMu.Unlock()
}

// pipe 双向拷贝。
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); closeWrite(a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); closeWrite(b); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// ── 协议编解码：null 分隔 JSON 帧 ──

func writeFrame(w io.Writer, data []byte) error {
	buf := make([]byte, 0, len(data)+1)
	buf = append(buf, data...)
	buf = append(buf, 0)
	_, err := w.Write(buf)
	return err
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	frame, err := r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	if len(frame) < 1 || len(frame) > maxFrameSize {
		return nil, fmt.Errorf("非法帧长度 %d", len(frame))
	}
	return frame[:len(frame)-1], nil
}

// parseHello 解析服务端 Hello 消息 → 公网端口。
func parseHello(frame []byte) (int, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frame, &m); err != nil {
		return 0, fmt.Errorf("无法解析握手响应: %s", string(frame))
	}
	if raw, ok := m["Error"]; ok {
		var s string
		_ = json.Unmarshal(raw, &s)
		return 0, fmt.Errorf("中继错误: %s", s)
	}
	if raw, ok := m["Challenge"]; ok {
		_ = raw
		return 0, errors.New("中继要求鉴权（bore.pub 无需鉴权，请检查地址）")
	}
	if raw, ok := m["Hello"]; ok {
		var port int
		if err := json.Unmarshal(raw, &port); err != nil {
			return 0, fmt.Errorf("握手响应端口异常: %s", string(raw))
		}
		return port, nil
	}
	return 0, fmt.Errorf("握手响应异常: %s", string(frame))
}

// parseServerMsg 解析服务端消息：返回 (类型, 连接ID, 错误文本)。
func parseServerMsg(frame []byte) (string, string, string, error) {
	var asStr string
	if err := json.Unmarshal(frame, &asStr); err == nil {
		if asStr == "Heartbeat" {
			return "Heartbeat", "", "", nil
		}
		return asStr, "", "", fmt.Errorf("未知消息: %s", asStr)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frame, &m); err != nil {
		return "", "", "", fmt.Errorf("无法解析消息: %s", string(frame))
	}
	for _, k := range []string{"Connection", "Hello", "Challenge", "Error", "Heartbeat"} {
		if raw, ok := m[k]; ok {
			switch k {
			case "Connection":
				var id string
				_ = json.Unmarshal(raw, &id)
				return k, id, "", nil
			case "Error":
				var s string
				_ = json.Unmarshal(raw, &s)
				return k, "", s, nil
			default:
				return k, "", "", nil
			}
		}
	}
	return "", "", "", fmt.Errorf("未知消息: %s", string(frame))
}
