package web

import (
	"embed"
	"net/http"
)

//go:embed www/index.html
var indexHTML []byte

//go:embed www/app.js
var appJS []byte

//go:embed www/captcha.js
var captchaJS []byte

//go:embed www/icons
var iconFS embed.FS

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#0f1115"/><path d="M18 20h28v6H36v22h-8V26H18z" fill="#4f8cff"/><circle cx="47" cy="43" r="5" fill="#39d98a"/></svg>`

// serveCaptchaJS 本机内置的阿里云验证码 SDK（官方 AliyunCaptcha.js，已打包进 exe）。
// 面板同源加载，不依赖外网 CDN，避免"验证码组件加载失败"。
func (p *Panel) serveCaptchaJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(captchaJS)
}

func (p *Panel) serveIcon(w http.ResponseWriter, r *http.Request) {	name := r.PathValue("name")
	data, err := iconFS.ReadFile("www/icons/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(data)
}
