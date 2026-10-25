// Package ollama 本机 Ollama 提供商。
// 直连 Ollama 自带的 OpenAI 兼容端点（/v1/chat/completions），tools / 多模态
// 文本请求原样透传；服务没起时自动在后台拉起 `ollama serve`（无窗口、托盘
// 不出现），用户全程无需打开 Ollama 软件。模型跑在本机 GPU/CPU 上，
// 这是它与 Trae/千问等云端额度渠道的本质区别。
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/httpx"
	"tokenhub/internal/logx"
	"tokenhub/internal/provider"
	"tokenhub/internal/store"
	"tokenhub/internal/winproc"
)

const defaultBase = "http://127.0.0.1:11434"

// Provider 实现 provider.Provider。无需凭据：池子里挂一个伪账号即可。
type Provider struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string { return store.POllama }

func (p *Provider) base() string {
	if pc := p.cfg.PC(store.POllama); pc != nil && pc.APIBase != "" {
		return pc.APIBase
	}
	return defaultBase
}

// ── 服务保活 ──

var startMu sync.Mutex

// ping 探测 Ollama 服务是否在。
func ping(ctx context.Context, base string) bool {
	var out map[string]any
	_, err := httpx.DoJSON(ctx, "GET", base+"/api/version", nil, nil, &out, 2*time.Second)
	return err == nil
}

// findExe 找 ollama 可执行文件（PATH → 常见安装位置）。
func findExe() string {
	for _, name := range []string{"ollama.exe", "ollama"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, env := range []string{"LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)"} {
		root := os.Getenv(env)
		if root == "" {
			continue
		}
		for _, cand := range []string{
			filepath.Join(root, "Programs", "Ollama", "ollama.exe"),
			filepath.Join(root, "Ollama", "ollama.exe"),
		} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	return ""
}

// ensureUp 确保本地 Ollama 服务在跑；没起就静默拉起 `ollama serve` 并等端口就绪。
func (p *Provider) ensureUp(ctx context.Context) error {
	base := p.base()
	if ping(ctx, base) {
		return nil
	}
	startMu.Lock()
	defer startMu.Unlock()
	if ping(ctx, base) { // 双重检查：并发请求可能已经拉起
		return nil
	}
	exe := findExe()
	if exe == "" {
		return provider.Err(provider.KindServer, 0,
			"本机未安装 Ollama：请到 ollama.com 安装后重试（或在设置中把 Ollama API 地址指向远程主机）")
	}
	cmd := winproc.Cmd(exe, "serve") // Windows 下隐藏控制台窗口，其它平台透传
	if err := cmd.Start(); err != nil {
		return provider.Err(provider.KindServer, 0, "Ollama 服务拉起失败: %v", err)
	}
	_ = cmd.Process.Release() // 常驻后台，不随 TokenHub 退出
	logx.Infof(store.POllama, "已后台拉起 Ollama 服务（%s serve）", exe)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return provider.Err(provider.KindNetwork, 0, "%v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
		if ping(ctx, base) {
			logx.Infof(store.POllama, "Ollama 服务已就绪: %s", base)
			return nil
		}
	}
	return provider.Err(provider.KindServer, 0, "Ollama 服务拉起超时（30 秒）：请手动打开一次 Ollama 检查是否正常")
}

// ── 模型 ──

func (p *Provider) SeedModels() []provider.ModelInfo { return nil } // 模型目录完全动态

// Models 列出已安装模型（/api/tags）。
func (p *Provider) Models(ctx context.Context, acct *store.Account) ([]provider.ModelInfo, error) {
	if err := p.ensureUp(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	status, err := httpx.DoJSON(ctx, "GET", p.base()+"/api/tags", nil, nil, &out, 15*time.Second)
	if err != nil {
		return nil, provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	if status >= 400 {
		return nil, provider.ClassifyBody(status, nil)
	}
	var list []provider.ModelInfo
	for _, m := range out.Models {
		if m.Name == "" {
			continue
		}
		list = append(list, provider.ModelInfo{ID: m.Name})
	}
	if len(list) == 0 {
		return nil, provider.Err(provider.KindServer, 0, "Ollama 尚未安装任何模型：可执行 ollama pull <模型名> 后重试")
	}
	return list, nil
}

// Quota 本地无额度概念：报服务状态与模型数。
func (p *Provider) Quota(ctx context.Context, acct *store.Account) (*store.QuotaSnapshot, error) {
	if err := p.ensureUp(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if _, err := httpx.DoJSON(ctx, "GET", p.base()+"/api/tags", nil, nil, &out, 15*time.Second); err != nil {
		return nil, provider.Err(provider.KindNetwork, 0, "%v", err)
	}
	return &store.QuotaSnapshot{
		Source:    "local",
		Plan:      fmt.Sprintf("%d 个模型", len(out.Models)),
		Note:      "本地推理，无额度限制",
		UpdatedAt: time.Now().Unix(),
	}, nil
}

func (p *Provider) SupportsCheckin(acct *store.Account) bool { return false }

func (p *Provider) Checkin(ctx context.Context, acct *store.Account) (bool, string, error) {
	return false, "", provider.ErrCheckinUnsupported
}

// Refresh 本地服务无凭据可刷。
func (p *Provider) Refresh(ctx context.Context, acct *store.Account) error { return nil }

// ── 对话 ──

// Chat 透传 OpenAI 格式请求（含 tools），强制流式；伪账号不参与鉴权。
func (p *Provider) Chat(ctx context.Context, acct *store.Account, openaiReq []byte, sink provider.Sink) error {
	if err := p.ensureUp(ctx); err != nil {
		sink.Close()
		return err
	}
	var body map[string]any
	if err := json.Unmarshal(openaiReq, &body); err != nil {
		sink.Close()
		return provider.Err(provider.KindClient, 400, "请求不是合法 JSON: %v", err)
	}
	body["stream"] = true
	pb, _ := json.Marshal(body)
	promptChars := estimatePrompt(body)
	h := map[string]string{"content-type": "application/json"}
	// 头部超时用上游墙钟（默认 600s）：Ollama 收到请求后要等模型载入显存才回
	// 响应头，首次冷启动可达 30~60 秒，15 秒连接超时会直接掐掉（实测 2026-10-18）。
	resp, err := httpx.DoStream(ctx, "POST", p.base()+"/v1/chat/completions", h, pb, p.cfg.UpstreamTimeout())
	if err != nil {
		sink.Close()
		// 连不上且服务刚被我们拉起过：给一句人话
		return provider.Err(provider.KindNetwork, 0, "Ollama 连接失败: %v", err)
	}
	if resp.Status >= 400 {
		sink.Close()
		return provider.ClassifyBody(resp.Status, resp.Body)
	}
	if resp.ContentType != "" && strings.Contains(resp.ContentType, "application/json") {
		// 2xx + JSON：业务错误信封（非 SSE）
		b, _ := io.ReadAll(io.LimitReader(resp.Stream, 1<<20))
		resp.Stream.Close()
		sink.Close()
		if len(strings.TrimSpace(string(b))) == 0 {
			return provider.Err(provider.KindServer, 200, "Ollama 返回空响应，请重试")
		}
		return provider.ClassifyBody(resp.Status, b)
	}
	return parseSSE(resp.Stream, sink, promptChars)
}

func estimatePrompt(body map[string]any) int {
	msgs, _ := body["messages"].([]any)
	n := 0
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			n += len([]rune(s))
		}
	}
	return n
}

// parseSSE 解析 Ollama 的 OpenAI 风格 SSE。
// 兼容 reasoning（新版 ollama 深度思考字段）与 reasoning_content（deepseek 风格）。
func parseSSE(r io.Reader, sink provider.Sink, promptChars int) error {
	defer sink.Close()
	firstRoleSent := false
	hasDelta := false
	finishReason := ""
	var usage *provider.Usage

	err := provider.ScanSSE(r, func(ev provider.SSEEvent) bool {
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			return data != "[DONE]"
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Role             string `json:"role"`
					Content          any    `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason any `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
			Error any    `json:"error"`
			Code  any    `json:"code"`
			Msg   string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return true
		}
		if chunk.Error != nil || (len(chunk.Choices) == 0 && chunk.Code != nil) {
			code, msg := extractError(chunk.Code, chunk.Error, chunk.Msg)
			sink.StreamError(code, msg)
			return false
		}
		if len(chunk.Choices) == 0 {
			return true
		}
		c := chunk.Choices[0]
		d := c.Delta
		delta := provider.Delta{}
		if d.Role != "" && !firstRoleSent {
			delta.Role = d.Role
			firstRoleSent = true
		}
		if s, ok := d.Content.(string); ok && s != "" {
			delta.Content = s
		}
		if delta.Reasoning = d.Reasoning; delta.Reasoning == "" {
			delta.Reasoning = d.ReasoningContent
		}
		for _, tc := range d.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			td := &provider.ToolDelta{Index: idx, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments}
			hasDelta = true
			sink.Delta(provider.Delta{Role: delta.Role, Tool: td})
			delta.Role = ""
		}
		if delta.Content != "" || delta.Reasoning != "" || delta.Role != "" {
			hasDelta = true
			sink.Delta(delta)
		}
		if fr, ok := c.FinishReason.(string); ok && fr != "" {
			finishReason = fr
		}
		if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
			u := chunk.Usage
			total := u.TotalTokens
			if total == 0 {
				total = u.PromptTokens + u.CompletionTokens
			}
			usage = &provider.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: total, Present: true}
		}
		return true
	})
	if err != nil {
		return err
	}
	if !hasDelta && usage == nil {
		sink.StreamError(0, "Ollama 返回空流（模型可能仍在加载，稍后重试）")
		return nil
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if usage == nil {
		prompt := int64(float64(promptChars) * 0.6)
		if prompt < 1 {
			prompt = 1
		}
		usage = &provider.Usage{PromptTokens: prompt, CompletionTokens: 1, TotalTokens: prompt + 1, Present: true}
	}
	sink.Finish(provider.Finish{Reason: finishReason, Usage: usage})
	return nil
}

func extractError(code any, errObj any, message string) (int64, string) {
	var codeN int64
	switch v := code.(type) {
	case float64:
		codeN = int64(v)
	case string:
		fmt.Sscanf(v, "%d", &codeN)
	}
	switch e := errObj.(type) {
	case string:
		if message == "" {
			message = e
		}
	case map[string]any:
		if m, ok := e["message"].(string); ok && message == "" {
			message = m
		}
	}
	if message == "" {
		message = "Ollama 上游错误"
	}
	return codeN, message
}
