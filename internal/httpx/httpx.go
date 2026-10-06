// Package httpx 上游 HTTP 工具：JSON 请求 / SSE 流式请求，统一超时与错误体读取。
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Transport 为全局共享的上游连接池。
// MaxIdleConnsPerHost 原为 8：并发流一多，空闲连接就被逐出，
// 每个请求都要重新 DNS+TCP+TLS 握手（国内网络 0.5~2 秒），
// 客户端等太久超时重试还会白白烧额度 —— 调大到 64 彻底避免。
var Transport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   64,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	DialContext: (&net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
}

// ── 连接预热 ──
// 上游空闲连接 90 秒后被回收，冷启动的请求必须重新握手。
// KeepWarm 定期对已注册的上游地址发一个轻量 HEAD（无鉴权，
// 返回 401/404/405 无所谓 —— 目的只是让连接池里始终有热连接），
// 让每次请求都能复用连接，省掉握手延迟，也减少客户端超时重试。
var (
	warmMu    sync.Mutex
	warmURLs  = map[string]bool{}
	warmOnce  sync.Once
)

// RegisterWarm 注册需要保持热连接的上游地址（重复注册自动去重）。
func RegisterWarm(urls ...string) {
	warmMu.Lock()
	added := false
	for _, u := range urls {
		if u == "" {
			continue
		}
		if !warmURLs[u] {
			warmURLs[u] = true
			added = true
		}
	}
	warmMu.Unlock()
	if added {
		warmOnce.Do(func() { go warmLoop() })
	}
}

func warmLoop() {
	tick := time.NewTicker(45 * time.Second)
	defer tick.Stop()
	warmAll() // 注册即预热一次，缩短启动后的首次握手
	for range tick.C {
		warmAll()
	}
}

func warmAll() {
	warmMu.Lock()
	urls := make([]string, 0, len(warmURLs))
	for u := range warmURLs {
		urls = append(urls, u)
	}
	warmMu.Unlock()
	for _, u := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err == nil {
			resp, err := Transport.RoundTrip(req)
			if err == nil && resp.Body != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
				resp.Body.Close()
			}
		}
		cancel()
	}
}

// Response 统一响应。
type Response struct {
	Status      int
	Header      http.Header
	Body        []byte      // 仅非流式
	Stream      io.ReadCloser // 仅流式
	ContentType string
}

func newRequest(ctx context.Context, method, url string, headers map[string]string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// Do 发送请求并读取完整响应体（受 ctx / timeout 双重限制）。
func Do(ctx context.Context, method, url string, headers map[string]string, body []byte, timeout time.Duration) (*Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := newRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data, ContentType: resp.Header.Get("Content-Type")}, nil
}

// DoJSON 发送 JSON 请求并解析 JSON 响应。
func DoJSON(ctx context.Context, method, url string, headers map[string]string, reqBody, out any, timeout time.Duration) (int, error) {
	var body []byte
	if reqBody != nil {
		switch v := reqBody.(type) {
		case []byte:
			body = v
		default:
			b, err := json.Marshal(reqBody)
			if err != nil {
				return 0, err
			}
			body = b
		}
	}
	r, err := Do(ctx, method, url, headers, body, timeout)
	if err != nil {
		return 0, err
	}
	if out != nil && len(r.Body) > 0 {
		if err := json.Unmarshal(r.Body, out); err != nil {
			return r.Status, fmt.Errorf("decode response: %w (body=%.120s)", err, string(r.Body))
		}
	}
	return r.Status, nil
}

// DoStream 发送请求；返回未读的响应流（调用方负责 Close）。
// 非 2xx 时读取错误体到 Body 并关闭流，Stream 为 nil。
// headerTimeout 只限制到响应头到达（连接段），不影响后续 SSE 流的存活。
//
// 性能关键点：必须复用共享 Transport（连接池），绝不能每请求 Clone——
// Clone 会得到空连接池，每个请求都重新 DNS+TCP+TLS 握手，白白多几百毫秒。
// 共享 Transport 不支持按请求设 ResponseHeaderTimeout，这里用「定时器
// 取消 ctx」模拟：响应头到达后立刻拆除定时器，之后流不受任何影响。
func DoStream(ctx context.Context, method, url string, headers map[string]string, body []byte, headerTimeout time.Duration) (*Response, error) {
	req, err := newRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	sctx, scancel := context.WithCancel(ctx)
	req = req.WithContext(sctx)
	var timer *time.Timer
	if headerTimeout > 0 {
		timer = time.AfterFunc(headerTimeout, scancel)
	}
	client := &http.Client{Transport: Transport}
	resp, err := client.Do(req)
	if err != nil {
		scancel()
		return nil, err
	}
	if timer != nil {
		timer.Stop() // 响应头已到达，拆除头部超时
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		scancel()
		return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data, ContentType: resp.Header.Get("Content-Type")}, nil
	}
	// ctx 生命周期与流绑定：流关闭时才释放（提前 cancel 会掐断 SSE 流）
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: scancel}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Stream: resp.Body, ContentType: resp.Header.Get("Content-Type")}, nil
}

// cancelBody 流关闭时同步释放请求 ctx。
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}
