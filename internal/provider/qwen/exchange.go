// 千问凭据交换：refresh_token → 短期 access token（约 15 分钟寿命）。
// 真前端口径（CDP 抓包确认）：
//
//	GET https://auth.qwen.ai/api/v2/auths/refresh
//	Cookie: refresh_token=<jwt>
//	x-request-origin: https://chat.qwen.ai  version: 0.3.19  source: web
//
// 响应 {"success":true,"data":{"access_token":..,"refresh_token":..}}（refresh 会轮换）。
// 注意：POST 到 chat.qwen.ai 的旧端点已不存在（返回软 404）。
package qwen

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tokenhub/internal/httpx"
	"tokenhub/internal/provider"
)

const authBase = "https://auth.qwen.ai"

// ExchangeResult 换取结果。
type ExchangeResult struct {
	Token        string // 新 access token（Bearer 用，~15 分钟）
	RefreshToken string // 上游轮换出的新 refresh token
}

// ExchangeRefresh 用 refresh token 换新的 access token。
func ExchangeRefresh(ctx context.Context, base, refreshToken string) (*ExchangeResult, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, fmt.Errorf("refresh token 为空")
	}
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Code         string `json:"code"`
			Details      string `json:"details"`
		} `json:"data"`
	}
	h := map[string]string{
		"cookie":            "refresh_token=" + rt,
		"accept":            "application/json, text/plain, */*",
		"source":            "web",
		"version":           version,
		"user-agent":        userAgent,
		"referer":           base + "/",
		"x-request-origin":  base,
		"x-request-id":      provider.NewUUID(),
		"timezone":          timezone,
		"sec-ch-ua":         `"Chromium";v="145", "Google Chrome";v="145", "Not?A_Brand";v="99"`,
		"sec-ch-ua-mobile":  "?0",
		"sec-ch-ua-platform": `"Windows"`,
	}
	status, err := httpx.DoJSON(ctx, "GET", authBase+"/api/v2/auths/refresh", h, nil, &out, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if status >= 400 || !out.Success || out.Data.AccessToken == "" {
		msg := out.Message
		if msg == "" {
			msg = out.Data.Details
		}
		if msg == "" {
			msg = out.Data.Code
		}
		if msg == "" {
			msg = fmt.Sprintf("status=%d", status)
		}
		return nil, fmt.Errorf("refresh 换取失败: %s", msg)
	}
	return &ExchangeResult{Token: out.Data.AccessToken, RefreshToken: out.Data.RefreshToken}, nil
}
