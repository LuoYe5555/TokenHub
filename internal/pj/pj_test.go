package pj

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyStringContent(t *testing.T) {
	body := []byte(`{"model":"qwen2.5:7b","stream":true,"messages":[{"role":"system","content":"you are helpful"},{"role":"user","content":"你好"}]}`)
	out := Apply("3_liner", body)
	var req map[string]any
	if json.Unmarshal(out, &req) != nil {
		t.Fatal("输出不是合法 JSON")
	}
	if req["model"] != "qwen2.5:7b" || req["stream"] != true {
		t.Fatal("无关字段丢失")
	}
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	c := last["content"].(string)
	if !strings.Contains(c, "你好") || !strings.Contains(c, "😈") {
		t.Fatalf("模板未正确包裹: %q", c)
	}
	// system 消息不能被动过
	if msgs[0].(map[string]any)["content"] != "you are helpful" {
		t.Fatal("system 消息被改动")
	}
}

func TestApplyArrayContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:..."}}]}]}`)
	out := Apply("3_liner", body)
	var req map[string]any
	json.Unmarshal(out, &req)
	msgs := req["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "hi") {
		t.Fatalf("数组形态未包裹: %q", text)
	}
	if len(content) != 2 {
		t.Fatal("非文本 part 被丢失")
	}
}

func TestApplyPlaceholderReplace(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"X"}]}`)
	out := Apply("3_liner", body)
	var req map[string]any
	json.Unmarshal(out, &req)
	c := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(c, "[INSERT PROMPT HERE]") {
		t.Fatal("占位符未被替换")
	}
}

func TestApplyOffAndUnknown(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	for _, id := range []string{"off", "not_exists"} {
		if got := Apply(id, body); string(got) != string(body) {
			t.Fatalf("模板 %q 不应改动请求", id)
		}
	}
}

func TestApplyNoUser(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"s"}]}`)
	if got := Apply("3_liner", body); string(got) != string(body) {
		t.Fatal("无 user 消息时不应改动")
	}
}

func TestGetAndIDs(t *testing.T) {
	if Get("aim") == nil || Get("off") != nil {
		t.Fatal("Get 行为异常")
	}
	ids := IDs()
	if ids[0] != "off" || len(ids) != len(Templates)+1 {
		t.Fatalf("IDs 异常: %v", ids)
	}
}

func TestLooksRefused(t *testing.T) {
	refused := []string{
		"I'm sorry, but I can't help with that.",
		"很抱歉，我无法提供这方面的内容。",
		"As an AI language model, I cannot assist.",
	}
	for _, s := range refused {
		if !LooksRefused(s) {
			t.Errorf("应判为拒答: %q", s)
		}
	}
	normal := []string{
		"[BetterDAN]: Sure thing, here is the recipe for sourdough bread.",
		"快速排序的时间复杂度是 O(n log n)。",
		"AIM: 第一步，准备面粉和水。",
	}
	for _, s := range normal {
		if LooksRefused(s) {
			t.Errorf("不应判为拒答: %q", s)
		}
	}
}
