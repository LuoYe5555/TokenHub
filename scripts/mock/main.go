// mock 三家上游服务，用于 TokenHub 本地联调（不访问真实上游）。
//   19001: WorkBuddy (OpenAI 风格)
//   19002: Trae SOLO
//   19003: ZCode (Anthropic 风格) + BigModel
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type sseEvent struct {
	event string
	data  string
}

func writeSSE(w http.ResponseWriter, events []sseEvent) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	for _, e := range events {
		if e.event != "" {
			fmt.Fprintf(w, "event: %s\n", e.event)
		}
		fmt.Fprintf(w, "data: %s\n\n", e.data)
		fl.Flush()
	}
}

func chunk(model string, delta map[string]any) sseEvent {
	raw, _ := json.Marshal(map[string]any{
		"id": "c1", "object": "chat.completion.chunk", "model": model,
		"choices": []map[string]any{{"index": 0, "delta": delta}},
	})
	return sseEvent{data: string(raw)}
}

func rawChunk(model, raw string) sseEvent {
	return sseEvent{data: strings.Replace(raw, "\"model\":\"\"", "\"model\":\""+model+"\"", 1)}
}

func mockJWT(sub, iss string) string {
	h := "eyJhbGciOiJIUzI1NiJ9"
	payload, _ := json.Marshal(map[string]any{"sub": sub, "exp": time.Now().Add(30 * 24 * time.Hour).Unix(), "iss": iss})
	return h + "." + b64url(payload) + ".sig"
}

func b64url(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		sb.WriteByte(alphabet[int(b[i])%64])
	}
	return sb.String()
}

func main() {
	addr := flag.String("addr", "127.0.0.1", "bind")
	flag.Parse()
	go mockWorkBuddy(*addr + ":19001")
	go mockTrae(*addr + ":19002")
	go mockZCode(*addr + ":19003")
	fmt.Println("mock upstreams on :19001 :19002 :19003")
	select {}
}

// ── WorkBuddy ──

func mockWorkBuddy(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/plugin/auth/state", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"state": "mock-state", "authUrl": "https://example.com/login"}})
	})
	mux.HandleFunc("GET /v2/plugin/auth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"accessToken": mockJWT("u123", "https://sso-ent1.example"), "refreshToken": "rt-1", "expiresIn": 86400,
		}})
	})
	mux.HandleFunc("GET /v2/plugin/login/account", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"uid": "u123", "enterpriseId": "ent1", "nickname": "MockWB"}})
	})
	mux.HandleFunc("POST /v2/plugin/auth/token/refresh", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accessToken": mockJWT("u123", ""), "expiresIn": 86400}})
	})
	mux.HandleFunc("POST /v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		var events []sseEvent
		if _, hasTools := body["tools"]; hasTools {
			events = []sseEvent{
				chunk(model, map[string]any{"role": "assistant", "tool_calls": []map[string]any{
					{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": "{\"city\":"}},
				}}),
				chunk(model, map[string]any{"tool_calls": []map[string]any{
					{"index": 0, "function": map[string]any{"arguments": " \"Beijing\"}"}},
				}}),
				rawChunk(model, `{"id":"c1","object":"chat.completion.chunk","model":"","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
				{data: "[DONE]"},
			}
		} else {
			events = []sseEvent{
				chunk(model, map[string]any{"role": "assistant"}),
				chunk(model, map[string]any{"content": "你好，"}),
				chunk(model, map[string]any{"content": "我是 WorkBuddy mock。"}),
				chunk(model, map[string]any{"reasoning_content": "思考中..."}),
				rawChunk(model, `{"id":"c1","object":"chat.completion.chunk","model":"","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}`),
				{data: "[DONE]"},
			}
		}
		writeSSE(w, events)
	})
	mux.HandleFunc("POST /v2/billing/meter/get-user-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"Response": map[string]any{"Data": map[string]any{
			"Accounts": []map[string]any{{"PackageName": "Mock Pro", "CycleCapacitySize": 1000, "CycleCapacityUsed": 300, "CycleCapacityRemain": 700}},
		}}}})
	})
	mux.HandleFunc("POST /v2/billing/meter/daily-checkin", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})
	mux.HandleFunc("GET /console/enterprises/personal/models", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"models": []map[string]any{
				{"id": "deepseek-v4-pro", "name": "DeepSeek V4 Pro", "credits": "x0.79 credits", "maxInputTokens": 128000, "maxOutputTokens": 16384},
				{"id": "glm-5.3", "name": "GLM 5.3", "credits": "x0.05 credits"},
			},
			"agents": map[string]any{"cli": map[string]any{"models": []string{"deepseek-v4-pro", "glm-5.3"}}},
		}})
	})
	fmt.Println("workbuddy mock", addr)
	http.ListenAndServe(addr, mux)
}

// ── Trae SOLO ──

func mockTrae(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cloudide/api/v3/trae/oauth/ExchangeToken", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{
			"Token": "trjwt-mock", "RefreshToken": "trrt-2", "TokenExpireAt": time.Now().Add(7 * 24 * time.Hour).UnixMilli(),
		}})
	})
	mux.HandleFunc("POST /cloudide/api/v3/trae/GetUserInfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"UserID": "tu1", "ScreenName": "MockTrae", "TenantID": "t1"}})
	})
	mux.HandleFunc("POST /api/agent/v3/llm_utils_chat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		var events []sseEvent
		if _, hasTools := body["tools"]; hasTools {
			events = []sseEvent{
				{event: "metadata", data: `{"session_id":"s1"}`},
				{event: "output", data: `{"tool_calls":[{"index":0,"id":"call_t1","type":"function","function_call":{"name":"get_weather","arguments":"{\"city\":"}}]}`},
				{event: "output", data: `{"tool_calls":[{"index":0,"function_call":{"arguments":" \"Beijing\"}"}}]}`},
				{event: "token_usage", data: `{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}`},
				{event: "done", data: `{"finish_reason":"tool_calls"}`},
			}
		} else {
			events = []sseEvent{
				{event: "metadata", data: `{"session_id":"s1"}`},
				{event: "output", data: `{"response":"你好，"}`},
				{event: "output", data: `{"response":"我是 Trae mock。"}`},
				{event: "output", data: `{"reasoning_content":"thinking..."}`},
				{event: "token_usage", data: `{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}`},
				{event: "done", data: `{"finish_reason":"stop"}`},
			}
		}
		writeSSE(w, events)
	})
	mux.HandleFunc("POST /api/ide/v1/get_detail_param", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"config_info_list": []map[string]any{
			{"config_name": "glm-5.2", "display_config": map[string]any{"display_name": "GLM 5.2"}},
			{"config_name": "Doubao-Seed-2.1-Pro", "display_config": map[string]any{"display_name": "Doubao Pro"}},
		}})
	})
	mux.HandleFunc("POST /trae/api/v2/pay/ide_user_ent_usage", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"user_entitlement_pack_list": []map[string]any{
			{"entitlement_base_info": map[string]any{"quota": map[string]any{"credits_limit": 2000}}, "usage": map[string]any{"credits_amount": 500}},
		}})
	})
	mux.HandleFunc("POST /trae/api/v2/ug/checkin_credits/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"checked_in": false, "credits": 200, "enable": true})
	})
	mux.HandleFunc("POST /trae/api/v2/ug/checkin_credits/claim", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "签到成功", "credits": 200})
	})
	fmt.Println("trae mock", addr)
	http.ListenAndServe(addr, mux)
}

// ── ZCode ──

func mockZCode(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/zcode-plan/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		if model == "" {
			model = "glm-5.3"
		}
		var events []sseEvent
		if _, hasTools := body["tools"]; hasTools {
			events = []sseEvent{
				{event: "message_start", data: `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"` + model + `","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`},
				{event: "content_block_start", data: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`},
				{event: "content_block_delta", data: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\": "}}`},
				{event: "content_block_delta", data: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Beijing\"}"}}`},
				{event: "content_block_stop", data: `{"type":"content_block_stop","index":0}`},
				{event: "message_delta", data: `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`},
				{event: "message_stop", data: `{"type":"message_stop"}`},
			}
		} else {
			events = []sseEvent{
				{event: "message_start", data: `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"` + model + `","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`},
				{event: "content_block_start", data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{event: "content_block_delta", data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好，"}}`},
				{event: "content_block_delta", data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我是 ZCode mock。"}}`},
				{event: "content_block_stop", data: `{"type":"content_block_stop","index":0}`},
				{event: "message_delta", data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`},
				{event: "message_stop", data: `{"type":"message_stop"}`},
			}
		}
		writeSSE(w, events)
	})
	mux.HandleFunc("GET /api/v1/zcode-plan/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"plans": []map[string]any{{
				"plan_id": "zcode-v3-start-plan", "name": "Start Plan", "status": "active",
				"ends_at": time.Now().Add(30 * 24 * time.Hour).Unix(),
				"entitlements": []map[string]any{
					// 与 balances 同名 → 应去重跳过
					{"entitlement_id": "model_usage", "show_name": "GLM-5.3", "unit_type": "token", "grant_units": 3000000, "period": "daily"},
					// 体验包：只出现在权益里 → 应被合并计入
					{"entitlement_id": "trial_glm53flash", "show_name": "GLM-5.3-Flash", "unit_type": "token", "grant_units": 500000, "period": "one_time", "effective_at": time.Now().Add(-1 * time.Hour).Unix()},
					// 待生效 → 不计入合计
					{"entitlement_id": "trial_pending", "show_name": "GLM-5-Turbo-体验", "unit_type": "token", "grant_units": 200000, "period": "one_time", "effective_at": time.Now().Add(24 * time.Hour).Unix()},
				},
			}},
			"balances": []map[string]any{{"entitlement_id": "model_usage", "show_name": "GLM-5.3", "unit_type": "token", "total_units": 3000000, "used_units": 1000000, "remaining_units": 2000000}},
		}})
	})
	mux.HandleFunc("GET /api/v1/zcode-plan/billing/current", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"plans": []map[string]any{
				// Start Plan：balance 里已有 → 按 plan_id 去重跳过
				{"plan_id": "zcode-v3-start-plan", "name": "Start Plan", "status": "active", "total_units": 3000000, "used_units": 1000000, "available_units": 2000000},
				// 独立领取的 1 亿体验包：balance 里没有 → 应合并计入
				{"plan_id": "zcode-trial-100m", "name": "GLM-5.3-Flash 体验包", "status": "active", "total_units": 100000000, "used_units": 45000000, "available_units": 55000000},
			},
		}})
	})
	mux.HandleFunc("GET /api/v1/zcode-plan/billing/preview", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"plans": []map[string]any{{"plan_id": "promo-1", "name": "Mock Promo", "priority": 10, "entitlements": []map[string]any{
				{"meter": "model_usage", "unit_type": "token", "show_name": "GLM-5.3", "grant_units": 500000, "period": "daily"},
			}}},
		}})
	})
	mux.HandleFunc("POST /api/v1/zcode-plan/billing/claim", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"plan": map[string]any{"name": "Mock Promo"}}})
	})
	mux.HandleFunc("GET /api/monitor/usage/quota/limit", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 500, "msg": "当前用户不存在coding plan"})
	})
	fmt.Println("zcode mock", addr)
	http.ListenAndServe(addr, mux)
}
