// TokenHub — 把 Trae / WorkBuddy(CodeBuddy) / ZCode 的额度转换为本地 OpenAI / Anthropic 兼容 API。
// 仅限个人自用：请在自己的账号内使用，勿用于商业用途。
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"tokenhub/internal/appicon"
	"tokenhub/internal/config"
	"tokenhub/internal/gateway"
	"tokenhub/internal/localimport"
	"tokenhub/internal/logx"
	"tokenhub/internal/pool"
	"tokenhub/internal/provider/trae"
	"tokenhub/internal/provider/workbuddy"
	"tokenhub/internal/provider/zcode"
	"tokenhub/internal/scheduler"
	"tokenhub/internal/shares"
	"tokenhub/internal/usagelog"
	"tokenhub/internal/store"
	"tokenhub/internal/tunnel"
	"tokenhub/internal/web"
	"tokenhub/internal/winicon"

	webview "github.com/jchv/go-webview2"
)

func main() {
	exePath, _ := os.Executable()
	defaultData := filepath.Join(filepath.Dir(exePath), "data")
	dataDir := flag.String("data", defaultData, "数据目录（config / accounts / logs）")
	host := flag.String("host", "", "监听地址（覆盖配置）")
	port := flag.Int("port", 0, "监听端口（覆盖配置）")
	noWindow := flag.Bool("no-window", false, "不打开窗口（纯后台服务）")
	noBrowser := flag.Bool("no-browser", false, "同 -no-window（兼容旧参数）")
	forceBrowser := flag.Bool("browser", false, "强制在系统浏览器打开面板")
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()

	if *showVersion {
		fmt.Println(config.AppName, config.AppVersion)
		return
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		winFatal("TokenHub 启动失败", "配置加载失败: "+err.Error())
		return
	}
	if *host != "" {
		cfg.Host = *host
	}
	if *port > 0 {
		cfg.Port = *port
	}
	logx.InitFile(filepath.Join(*dataDir, "logs"))

	st, err := store.Load(*dataDir)
	if err != nil {
		winFatal("TokenHub 启动失败", "账号存储加载失败: "+err.Error())
		return
	}

	// 提供商与账号池
	pl := pool.New(cfg, st)
	pl.Register(workbuddy.New(cfg))
	pl.Register(trae.New(cfg))
	pl.Register(zcode.New(cfg))
	zcode.WarmAppVersion()

	// 启动即扫描本机已登录的客户端账号（静默导入，按 uid 去重）
	go func() {
		added := map[string]int{}
		added[store.PWorkBuddy] = localimport.AddAllUnique(st, store.PWorkBuddy, localimport.WorkBuddyLocal())
		added[store.PTrae] = localimport.AddAllUnique(st, store.PTrae, localimport.TraeLocal())
		if accts := zcodeReadLocalAccounts(); len(accts) > 0 {
			added[store.PZCode] = localimport.AddAllUnique(st, store.PZCode, accts)
		}
		if n := added[store.PWorkBuddy] + added[store.PTrae] + added[store.PZCode]; n > 0 {
			logx.Infof("import", "本机自动导入完成: workbuddy +%d, trae +%d, zcode +%d",
				added[store.PWorkBuddy], added[store.PTrae], added[store.PZCode])
		}
	}()

	// 调度器（签到 / 领取 / 刷新）
	sched := scheduler.New(cfg, st, pl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)

	// 网关 + 分享钥匙 + 用量日志
	sh, err := shares.Load(*dataDir)
	if err != nil {
		winFatal("TokenHub 启动失败", "分享钥匙加载失败: "+err.Error())
		return
	}
	ulog := usagelog.Load(*dataDir)
	// 调用后自动同步额度：每次 API 调用完成后异步刷新一次全量快照（scheduler 内部去抖），
	// 保证用量页「今日 tokens」与各账号「已用」实时一致。
	gw := gateway.New(cfg, pl, sh, ulog)
	gw.AfterCall = func(provName string, ok bool) { sched.KickQuotaRefresh() }

	// 公网分享专用「仅 API 监听」：只挂网关路由，绑定 127.0.0.1，
	// 隧道只转发到这个端口 —— 面板 / 账号数据永远不会暴露到公网。
	tunnelMgr := tunnel.New(0)
	apiOnlyBound := false
	for try := cfg.Port + 1; try <= cfg.Port+50; try++ {
		muxAPI := http.NewServeMux()
		gw.Register(muxAPI)
		lnAPI, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(try)))
		if err != nil {
			continue
		}
		apiOnlyBound = true
		srvAPI := &http.Server{Handler: muxAPI, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
		go func() {
			if err := srvAPI.Serve(lnAPI); err != nil && err != http.ErrServerClosed {
				logx.Errorf("main", "仅 API 监听异常退出: %v", err)
			}
		}()
		tunnelMgr = tunnel.New(try)
		logx.Infof("main", "公网分享专用监听: 127.0.0.1:%d（仅 API，需密钥）", try)
		break
	}
	if !apiOnlyBound {
		logx.Warnf("main", "未找到可用的仅 API 监听端口，公网分享不可用")
	}

	panel := web.NewPanel(cfg, st, pl, sched, sh, tunnelMgr, ulog)
	mux := http.NewServeMux()
	gw.Register(mux)
	panel.Register(mux)

	// 先绑定端口再开窗口，避免窗口先加载打空
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		winFatal("TokenHub 启动失败", "端口 "+fmt.Sprint(cfg.Port)+" 被占用（可能已有一个 TokenHub 在运行）。\n\n可换端口启动: TokenHub.exe -port 8688")
		return
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logx.Errorf("main", "HTTP 服务异常退出: %v", err)
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	logx.Infof("main", "面板 %s | API %s/v1 | 数据目录 %s", baseURL, baseURL, *dataDir)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	windowMode := !*noWindow && !*noBrowser && !*forceBrowser && runtime.GOOS == "windows"
	if windowMode {
		w := webview.NewWithOptions(webview.WebViewOptions{
			Debug:     false,
			AutoFocus: true,
			WindowOptions: webview.WindowOptions{
				Title:  config.AppName,
				Width:  1220,
				Height: 840,
				Center: true,
			},
		})
		w.SetTitle(config.AppName)
		w.Dispatch(func() { w.Navigate(baseURL) })
		defer w.Destroy()
		go func() {
			select {
			case <-quit:
				w.Terminate()
			case <-ctx.Done():
				w.Terminate()
			}
		}()
		// 窗口图标：-icon 参数 > 配置 windowIcon > 内置图标
		applyWindowIcon(w, *dataDir)
		// webview 桥：自动领取撞验证码时自动弹窗 + SendInput 自动滑块（全程免人工）
		panel.SetWebview(w.Dispatch, func(js string) { w.Eval(js) }, uintptr(w.Window()))
		w.Run() // 阻塞直到窗口关闭
		logx.Infof("main", "窗口已关闭，正在退出…")
		shutdown(srv)
		return
	} else if *forceBrowser {
		openBrowser(baseURL)
	}
	<-quit
	logx.Infof("main", "正在退出…")
	shutdown(srv)
}

func shutdown(srv *http.Server) {
	sdCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sdCtx)
}

// winFatal GUI 模式下的错误提示（无控制台也能看到）。
func winFatal(title, msg string) {
	logx.Errorf("main", "%s: %s", title, msg)
	messageBox(title, msg)
	fmt.Fprintln(os.Stderr, title+":", msg)
}

func messageBox(title, text string) {
	if runtime.GOOS != "windows" {
		return
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	mb := user32.NewProc("MessageBoxW")
	t16, _ := syscall.UTF16PtrFromString(title)
	m16, _ := syscall.UTF16PtrFromString(text)
	_, _, _ = mb.Call(0, uintptr(unsafe.Pointer(m16)), uintptr(unsafe.Pointer(t16)), 0x10)
}

// zcodeReadLocalAccounts 本机 ZCode 当前登录 → 账号（启动自动扫描用）。
func zcodeReadLocalAccounts() []*store.Account {
	creds, ok, err := zcode.ReadLocal()
	if err != nil || !ok {
		return nil
	}
	acct := creds.ToAccount()
	if acct == nil {
		return nil
	}
	return []*store.Account{acct}
}

// applyWindowIcon 图标已全部内置：标题栏用内置 app.png（用户自定义狗图），
// 任务栏固定用内置品牌 "T" 图。不再支持外部图片路径配置。
func applyWindowIcon(w webview.WebView, dataDir string) {
	smallImg, err := appicon.Image()
	if err != nil {
		logx.Warnf("main", "内置标题栏图解码失败: %v", err)
		return
	}
	bigImg, err := appicon.BrandImage()
	if err != nil {
		logx.Warnf("main", "内置品牌图解码失败: %v", err)
		bigImg = smallImg
	}
	if smallImg == nil || bigImg == nil {
		return
	}
	smallPath := filepath.Join(dataDir, "window-icon.ico")
	if err := winicon.WriteICOFile(smallPath, smallImg); err != nil {
		logx.Warnf("main", "窗口图标生成失败: %v", err)
		return
	}
	bigPath := filepath.Join(dataDir, "taskbar-icon.ico")
	if err := winicon.WriteICOFile(bigPath, bigImg); err != nil {
		logx.Warnf("main", "任务栏图标生成失败: %v", err)
		return
	}
	// 窗口已创建（NewWithOptions 内部完成），同线程 SendMessage 不依赖消息循环
	if err := winicon.Apply(w.Window(), smallPath, bigPath); err != nil {
		logx.Warnf("main", "窗口图标设置失败: %v", err)
	} else {
		logx.Infof("main", "窗口图标已应用（标题栏 内置 / 任务栏 T）")
	}
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
