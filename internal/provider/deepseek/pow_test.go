package deepseek

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// 测试向量来自 go-deepseek-web 社区实现（与官方 wasm 行为实测一致）。
func TestDeepSeekHashV1(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "d3861d3283d610f532813c30dd15162c6980aed6d34f05aa26caac9baf306f95"},
		{"test_salt_1700000000_1234", "a406aa0b3c998413fbe1d6a7816992f4d7629395c768022af5f52e3871a17c68"},
	}
	for _, tt := range tests {
		got := DeepSeekHashV1([]byte(tt.input))
		if h := hexEncode(got); h != tt.expected {
			t.Errorf("DeepSeekHashV1(%q) = %s; want %s", tt.input, h, tt.expected)
		}
	}
}

func TestSolvePow(t *testing.T) {
	ch := Challenge{
		Algorithm:  "DeepSeekHashV1",
		Challenge:  "a406aa0b3c998413fbe1d6a7816992f4d7629395c768022af5f52e3871a17c68",
		Salt:       "test_salt",
		Difficulty: 2000,
		ExpireAt:   1700000000,
		Signature:  "sig123",
		TargetPath: "/api/v0/chat/completion",
	}
	res, err := SolvePow(ch)
	if err != nil {
		t.Fatalf("SolvePow error: %v", err)
	}
	raw, derr := base64.StdEncoding.DecodeString(res)
	if derr != nil {
		t.Fatalf("base64 decode error: %v", derr)
	}
	var sol map[string]any
	if err := json.Unmarshal(raw, &sol); err != nil {
		t.Fatalf("json unmarshal error: %v", err)
	}
	if sol["answer"] != float64(1234) {
		t.Errorf("sol.answer = %v; want 1234", sol["answer"])
	}
}

func BenchmarkSolvePow144k(b *testing.B) {
	target := hexEncode(DeepSeekHashV1([]byte("bench_salt_1700000000_100000")))
	ch := Challenge{
		Algorithm: "DeepSeekHashV1", Challenge: target, Salt: "bench_salt",
		Difficulty: 144000, ExpireAt: 1700000000, Signature: "sig",
		TargetPath: "/api/v0/chat/completion",
	}
	for b.Loop() {
		if _, err := SolvePow(ch); err != nil {
			b.Fatal(err)
		}
	}
}

func hexEncode(b [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
