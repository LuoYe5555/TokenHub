package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"tokenhub/internal/logx"
)

// rpcTimeout 每次插件调用的时间上限（防死循环）。
const rpcTimeout = 5 * time.Second

// Runtime 一个已加载插件的 goja 沙箱实例。
type Runtime struct {
	mu  sync.Mutex
	vm  *goja.Runtime
	man *Manifest
	dir string
	m   *Manager
	hc  *http.Client
}

// newRuntime 创建 VM、注入 SDK、执行入口脚本。
func newRuntime(m *Manager, man *Manifest, dir string) (*Runtime, error) {
	rt := &Runtime{man: man, dir: dir, m: m, hc: &http.Client{Timeout: 8 * time.Second}}
	vm := goja.New()
	rt.vm = vm

	// th = 插件 SDK（同步 API；goja 无并发，调用期间 VM 被锁住）
	_ = vm.Set("th", map[string]any{
		"log": func(args ...any) {
			parts := make([]string, len(args))
			for i, a := range args {
				parts[i] = fmt.Sprint(a)
			}
			logx.Infof("plugin/"+man.ID, "%s", strings.Join(parts, " "))
		},
		"storage": map[string]any{
			"get": rt.storageGet,
			"set": rt.storageSet,
		},
		"data": map[string]any{
			// models() 返回模型目录快照 {provider: [{id,name}]}
			"models": func() any {
				if m.DataFn == nil {
					return map[string]any{}
				}
				return m.DataFn()
			},
		},
		"http": map[string]any{
			// fetch(url, opts) 同步请求；origin 必须在 permissions.network 白名单里
			"fetch": rt.httpFetch,
		},
	})

	src, err := readFileBytes(dir, man.Main)
	if err != nil {
		return nil, err
	}
	if _, err := vm.RunString(string(src)); err != nil {
		return nil, fmt.Errorf("入口脚本执行失败: %w", err)
	}
	logx.Infof("plugins", "已启动插件 %s v%s", man.ID, man.Version)
	return rt, nil
}

// stop 停止（goja VM 无常驻线程，丢弃引用即可）。
func (rt *Runtime) stop() {
	logx.Infof("plugins", "已停止插件 %s", rt.man.ID)
}

// storageGet / storageSet 键值存储（JSON 全量落盘，值须 JSON 可序列化）。
func (rt *Runtime) storageGet(key string) any {
	raw, err := readFileBytes(rt.m.root, "data/"+rt.man.ID+".json")
	if err != nil {
		return goja.Undefined()
	}
	var kv map[string]any
	if json.Unmarshal(raw, &kv) != nil {
		return goja.Undefined()
	}
	v, ok := kv[key]
	if !ok {
		return goja.Undefined()
	}
	return v
}

func (rt *Runtime) storageSet(key string, val any) {
	raw, _ := readFileBytes(rt.m.root, "data/"+rt.man.ID+".json")
	var kv map[string]any
	_ = json.Unmarshal(raw, &kv)
	if kv == nil {
		kv = map[string]any{}
	}
	kv[key] = val
	out, err := json.Marshal(kv)
	if err != nil {
		panic(rt.vm.NewGoError(fmt.Errorf("存储值不可序列化: %v", err)))
	}
	if err := writeFileBytes(rt.m.root, "data/"+rt.man.ID+".json", out); err != nil {
		panic(rt.vm.NewGoError(err))
	}
}

// httpFetch 白名单限定的同步 HTTP。
func (rt *Runtime) httpFetch(url string, opts map[string]any) map[string]any {
	origin := originOf(url)
	if !rt.originAllowed(origin) {
		panic(rt.vm.NewGoError(fmt.Errorf("origin 不在 permissions.network 白名单: %s", origin)))
	}
	method := "GET"
	var body io.Reader
	if opts != nil {
		if m, _ := opts["method"].(string); m != "" {
			method = strings.ToUpper(m)
		}
		if b, ok := opts["body"]; ok {
			switch v := b.(type) {
			case string:
				body = strings.NewReader(v)
			default:
				raw, _ := json.Marshal(v)
				body = strings.NewReader(string(raw))
			}
		}
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		panic(rt.vm.NewGoError(err))
	}
	if opts != nil {
		if hs, ok := opts["headers"].(map[string]any); ok {
			for k, v := range hs {
				req.Header.Set(k, fmt.Sprint(v))
			}
		}
	}
	resp, err := rt.hc.Do(req)
	if err != nil {
		panic(rt.vm.NewGoError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return map[string]any{
		"status": resp.StatusCode,
		"body":   string(raw),
	}
}

func (rt *Runtime) originAllowed(origin string) bool {
	for _, o := range rt.man.Permissions.Network {
		if o == origin {
			return true
		}
	}
	return false
}

func originOf(rawURL string) string {
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else {
		return ""
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s // host[:port]
}

// Call 同步调用插件全局 onRpc(method, params)，带超时中断。
func (rt *Runtime) Call(method string, params any) (out any, err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	fn, ok := goja.AssertFunction(rt.vm.Get("onRpc"))
	if !ok {
		return nil, fmt.Errorf("插件未定义 onRpc")
	}
	timer := time.AfterFunc(rpcTimeout, func() { rt.vm.Interrupt("onRpc 超时（" + rpcTimeout.String() + "）") })
	defer timer.Stop()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("插件执行异常: %v", r)
		}
	}()
	var pv goja.Value
	if params != nil {
		pv = rt.vm.ToValue(params)
	} else {
		pv = goja.Undefined()
	}
	res, err := fn(goja.Undefined(), rt.vm.ToValue(method), pv)
	if err != nil {
		return nil, err
	}
	out, err = jsonValue(rt.vm, res)
	if err != nil {
		return nil, err
	}
	// 插件约定：返回 {error: "..."} 表示业务错误
	if m, ok := out.(map[string]any); ok {
		if emsg, ok := m["error"].(string); ok && emsg != "" {
			return nil, fmt.Errorf("%s", emsg)
		}
	}
	return out, nil
}

// jsonValue 把 goja 值转成 Go 值（经 JSON stringify/parse，剥掉 VM 包装）。
func jsonValue(vm *goja.Runtime, v goja.Value) (any, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, nil
	}
	s, ok := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	if !ok {
		return nil, fmt.Errorf("JSON.stringify 不可用")
	}
	raw, err := s(goja.Undefined(), v)
	if err != nil {
		return nil, err
	}
	var out any
	if json.Unmarshal([]byte(raw.String()), &out) != nil {
		return nil, fmt.Errorf("返回值不可 JSON 序列化")
	}
	return out, nil
}
