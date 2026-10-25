package updater

import (
	"os"
	"testing"

	"tokenhub/internal/config"
)

func TestTagCommit(t *testing.T) {
	cases := map[string]string{
		"release-3c4d33f":       "3c4d33f",
		"release-abc1234":       "abc1234",
		"fix-login-bug-9876543": "9876543", // CI 的 <说明>-<sha> 格式
		"v1.2.3":                "v1.2.3",  // 非 commit 结尾的 tag 原样返回（与本地 commit 必不相等，视作有更新）
	}
	for tag, want := range cases {
		if got := tagCommit(tag); got != want {
			t.Errorf("tagCommit(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestNormalizedCommit(t *testing.T) {
	orig := config.GitCommit
	defer func() { config.GitCommit = orig }()
	config.GitCommit = "3C4D33F"
	if got := normalizedCommit(); got != "3c4d33f" {
		t.Errorf("normalizedCommit() = %q, want 3c4d33f", got)
	}
	config.GitCommit = ""
	if got := normalizedCommit(); got != "dev" {
		t.Errorf("empty commit should be dev, got %q", got)
	}
}

// TestLatestLive 联网验证 latest release 查询（直连+镜像链），不下载不替换。
func TestLatestLive(t *testing.T) {
	if os.Getenv("UPD_LIVE") == "" {
		t.Skip("需要 UPD_LIVE=1")
	}
	m := &Manager{}
	tag, url, err := m.latest()
	if err != nil {
		t.Fatalf("latest 失败: %v", err)
	}
	t.Logf("latest tag=%s asset=%s", tag, url)
	if tag == "" || url == "" {
		t.Fatal("tag 或 asset URL 为空")
	}
	if tagCommit(tag) == "" {
		t.Fatalf("tag %s 解析不出 commit", tag)
	}
}
