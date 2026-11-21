package deepseek

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 超长折叠：elideMiddle 产出合法 UTF-8 且不超过预算；shrink 丢老留新。
func TestElideMiddleAndShrink(t *testing.T) {
	// elideMiddle：中文内容掐中间，不切坏字符，不超预算
	long := strings.Repeat("这是超长填充内容。", 20000) // ~28 万字节
	for _, budget := range []int{32000, 5000, 300} {
		got := elideMiddle(long, budget)
		if !utf8.ValidString(got) {
			t.Fatalf("budget=%d 结果不是合法 UTF-8", budget)
		}
		if len(got) > budget {
			t.Fatalf("budget=%d 实际 %d 字节，超出预算", budget, len(got))
		}
		if !strings.Contains(got, "中间内容过长已截断") {
			t.Fatalf("budget=%d 缺少截断标记", budget)
		}
		// 掐头留尾：开头和结尾都保留
		if !strings.HasPrefix(got, "这是超长填充内容。") || !strings.HasSuffix(got, "这是超长填充内容。") {
			t.Fatalf("budget=%d 未保留开头/结尾", budget)
		}
	}
	// 短文本原样返回
	if got := elideMiddle("短文本", 32000); got != "短文本" {
		t.Fatalf("短文本被改动: %q", got)
	}

	// shrink：丢最老的 turn、保留 system 与最后一个 turn，压到预算内
	r := &parsedReq{
		system: "你是测试系统提示。",
		turns: []string{
			strings.Repeat("老历史甲。", 20000),
			strings.Repeat("老历史乙。", 20000),
			strings.Repeat("老历史丙。", 20000),
			"最后一条用户请求",
		},
	}
	r.rebuild()
	r.shrink(50 * 1024)
	if len(r.content) > 50*1024 {
		t.Fatalf("shrink 后仍超预算: %d", len(r.content))
	}
	if !strings.Contains(r.content, "你是测试系统提示。") {
		t.Fatalf("system 被丢弃")
	}
	if !strings.Contains(r.content, "最后一条用户请求") {
		t.Fatalf("最后一个 turn 被丢弃")
	}
	if !strings.Contains(r.content, "老历史丙。") || strings.Contains(r.content, "老历史甲。") || strings.Contains(r.content, "老历史乙。") {
		t.Fatalf("应从最老的 turn 开始丢弃（只留丙与最后请求），实际长度 %d", len(r.content))
	}

	// shrink：只剩最后一个 turn 仍超限时，对它单独掐中间
	r2 := &parsedReq{
		turns: []string{strings.Repeat("唯一超长请求。", 60000)},
	}
	r2.rebuild()
	r2.shrink(64 * 1024)
	if len(r2.content) > 64*1024 {
		t.Fatalf("单 turn 收缩后仍超预算: %d", len(r2.content))
	}
	if !strings.Contains(r2.content, "中间内容过长已截断") {
		t.Fatalf("单 turn 未触发中间截断")
	}
}
