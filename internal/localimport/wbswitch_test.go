package localimport

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"tokenhub/internal/store"
)

// wbSwitchSample wb-switch 导出格式的最小真实形状（token 为占位符）。
const wbSwitchSample = `[
  {
    "access_token": "at-1",
    "refresh_token": "rt-1",
    "token_type": "Bearer",
    "uid": "uid-a",
    "nickname": "13900003352",
    "domain": "www.codebuddy.cn",
    "variant": "cn",
    "email": null,
    "enterpriseId": null,
    "id": "local-rec-1",
    "createdAt": 1790175008075,
    "refreshedAt": 1790300389400,
    "expiresAt": 1795052389400,
    "refreshExpiresAt": 1795484389400,
    "auth_raw": {
      "accessToken": "at-1",
      "refreshToken": "rt-1",
      "domain": "www.codebuddy.cn",
      "expiresIn": 4751999,
      "refreshExpiresIn": 5183999,
      "sessionState": "sess-1"
    },
    "profile_raw": { "uid": "uid-a", "nickname": "13900003352", "phoneNumber": "13900003352", "uin": "330000224" }
  },
  {
    "access_token": "at-2",
    "refresh_token": "rt-2",
    "uid": "uid-b",
    "nickname": "mc506lw",
    "domain": "www.workbuddy.ai",
    "variant": "intl",
    "expiresAt": 1795052389608,
    "refreshExpiresAt": 1795484389608,
    "auth_raw": { "sessionState": "sess-2" },
    "profile_raw": { "uid": "uid-b", "uin": "330000093" }
  }
]`

func TestParseWBSwitchFile(t *testing.T) {
	entries, err := ParseWBSwitchFile([]byte(wbSwitchSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	a := entries[0]
	if a.Err != "" || a.Account == nil {
		t.Fatalf("entry 0 invalid: %s", a.Err)
	}
	if a.Account.UID != "uid-a" || a.Account.Realm != "cn" || a.Account.AccessToken != "at-1" || a.Account.RefreshToken != "rt-1" {
		t.Fatalf("unexpected account: %+v", a.Account)
	}
	if a.Account.ExpiresAt != 1795052389 { // 毫秒 → 秒
		t.Fatalf("expiresAt = %d, want 1795052389", a.Account.ExpiresAt)
	}
	if a.Account.Domain != "www.codebuddy.cn" || a.Account.Nickname != "13900003352" {
		t.Fatalf("domain/nickname wrong: %+v", a.Account)
	}
	if a.Account.ExtraGet(store.ExtraWBSessionState) != "sess-1" || a.Account.ExtraGet(store.ExtraWBUin) != "330000224" {
		t.Fatalf("extra not captured: %+v", a.Account.Extra)
	}

	// variant=intl → realm=global
	b := entries[1]
	if b.Account == nil || b.Account.Realm != "global" {
		t.Fatalf("intl variant should map to global, got %+v", b.Account)
	}
	if b.Account.Provider != store.PWorkBuddy {
		t.Fatalf("provider = %q", b.Account.Provider)
	}
}

func TestParseWBSwitchVariants(t *testing.T) {
	// 缺 variant 时按域名推断
	got, err := ParseWBSwitchFile([]byte(`[{"access_token":"x","uid":"u1","domain":"www.workbuddy.ai"}]`))
	if err != nil || len(got) != 1 {
		t.Fatalf("parse: %v %d", err, len(got))
	}
	if got[0].Account == nil || got[0].Account.Realm != "global" {
		t.Fatalf("domain should imply global: %+v", got[0])
	}
	if len(got[0].Warnings) == 0 { // 没有 refreshToken 应给出提示
		t.Fatal("expected warning for missing refresh token")
	}

	// camelCase 键名同样接受
	got, err = ParseWBSwitchFile([]byte(`{"accounts":[{"accessToken":"x","refreshToken":"y","uid":"u2","realm":"cn"}]}`))
	if err != nil || len(got) != 1 {
		t.Fatalf("camelCase parse: %v %d", err, len(got))
	}
	if got[0].Account == nil || got[0].Account.UID != "u2" || got[0].Account.RefreshToken != "y" {
		t.Fatalf("camelCase mapping wrong: %+v", got[0].Account)
	}

	// 缺 uid → 从 JWT sub 推断
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"uid-from-jwt"}`))
	tok := "eyJhbGciOiJub25lIn0." + payload + ".sig"
	got, err = ParseWBSwitchFile([]byte(`[{"access_token":"` + tok + `"}]`))
	if err != nil || len(got) != 1 {
		t.Fatalf("jwt parse: %v %d", err, len(got))
	}
	if got[0].Account == nil || got[0].Account.UID != "uid-from-jwt" {
		t.Fatalf("uid should come from JWT: %+v", got[0])
	}

	// 既无 token 也无 uid → invalid
	got, err = ParseWBSwitchFile([]byte(`[{"nickname":"x"},{"access_token":"a"}]`))
	if err != nil || len(got) != 2 {
		t.Fatalf("invalid rows: %v %d", err, len(got))
	}
	if got[0].Err == "" {
		t.Fatal("row without token should be invalid")
	}
}

func TestParseWBSwitchBadInput(t *testing.T) {
	if _, err := ParseWBSwitchFile([]byte("  ")); err == nil {
		t.Fatal("empty input should fail")
	}
	if _, err := ParseWBSwitchFile([]byte(`"just a string"`)); err == nil {
		t.Fatal("non-array input should fail")
	}
	if _, err := ParseWBSwitchFile([]byte(`[{`)); err == nil {
		t.Fatal("broken JSON should fail")
	}
}

func TestApplyWBSwitch(t *testing.T) {
	st, err := store.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseWBSwitchFile([]byte(wbSwitchSample))
	if err != nil {
		t.Fatal(err)
	}

	// 预演：不落盘
	rep := ApplyWBSwitch(st, entries, true)
	if !rep.DryRun || rep.Added != 2 || rep.Updated != 0 || len(st.All()) != 0 {
		t.Fatalf("dry run should not write: %+v, accounts=%d", rep, len(st.All()))
	}

	// 正式导入
	rep = ApplyWBSwitch(st, entries, false)
	if rep.Added != 2 || rep.Updated != 0 || len(st.ByProvider(store.PWorkBuddy)) != 2 {
		t.Fatalf("import failed: %+v", rep)
	}

	// 再次导入相同内容 → 全部跳过
	entries2, _ := ParseWBSwitchFile([]byte(wbSwitchSample))
	rep = ApplyWBSwitch(st, entries2, false)
	if rep.Skipped != 2 || rep.Added != 0 || len(st.ByProvider(store.PWorkBuddy)) != 2 {
		t.Fatalf("duplicate import should skip: %+v", rep)
	}

	// token 变化 → 更新同一条（ID 不变）
	before := st.ByProvider(store.PWorkBuddy)[0]
	var rows []map[string]any
	if err := json.Unmarshal([]byte(wbSwitchSample), &rows); err != nil {
		t.Fatal(err)
	}
	rows[0]["access_token"] = "at-1-new"
	rows[0]["refresh_token"] = "rt-1-new"
	b, _ := json.Marshal(rows)
	entries3, _ := ParseWBSwitchFile(b)
	rep = ApplyWBSwitch(st, entries3, false)
	if rep.Updated != 1 || rep.Skipped != 1 {
		t.Fatalf("changed token should update: %+v", rep)
	}
	after := st.Get(before.ID)
	if after == nil || after.AccessToken != "at-1-new" {
		t.Fatalf("account not updated in place: %+v", after)
	}
	if len(st.ByProvider(store.PWorkBuddy)) != 2 {
		t.Fatal("update must not create a new account")
	}
}
