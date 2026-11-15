package gateway

import (
	"testing"

	wire "tokenhub/internal/oc/wire"
)

func chatTool(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":       name,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// 精确候选名单仍然优先：Claude Code 风格的 Bash 直接绑到 shell/execute。
func TestBindOfficialExactCandidates(t *testing.T) {
	payload := map[string]any{"tools": []any{chatTool("Bash")}}
	renames, ok := RenameAndCompleteTools(payload, wire.Chat, false)
	if !ok {
		t.Fatal("RenameAndCompleteTools failed")
	}
	if client, ok := renames.Official("shell"); !ok || client != "Bash" {
		t.Fatalf("shell bound to %q (want Bash)", client)
	}
}

// 关键词兜底：客户端把 shell 工具命名为 run_command（WorkBuddy 风格），
// 官方 shell/execute 的调用必须还原回 run_command，否则客户端报 unknown tool。
func TestBindOfficialKeywordAliases(t *testing.T) {
	payload := map[string]any{"tools": []any{chatTool("run_command"), chatTool("read_file"), chatTool("web_search")}}
	renames, ok := RenameAndCompleteTools(payload, wire.Chat, false)
	if !ok {
		t.Fatal("RenameAndCompleteTools failed")
	}
	checks := map[string]string{
		"shell":     "run_command",
		"execute":   "run_command",
		"read":      "read_file",
		"websearch": "web_search",
	}
	for official, want := range checks {
		if client, ok := renames.Official(official); !ok || client != want {
			t.Fatalf("%s bound to %q,%v (want %q)", official, client, ok, want)
		}
	}
	if _, ok := renames.Resolve("shell"); !ok {
		t.Fatal("Resolve(shell) should report a rename")
	}
	if client, _ := renames.Resolve("shell"); client != "run_command" {
		t.Fatalf("Resolve(shell) = %q (want run_command)", client)
	}
}
