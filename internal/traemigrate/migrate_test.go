package traemigrate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// makeFixtureDB 构造模拟 Trae AI 库结构：project / session_project / chat_session / local_artifact。
func makeFixtureDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE project (project_id TEXT PRIMARY KEY, name TEXT, absolute_path TEXT, user_id TEXT)`,
		`CREATE TABLE session_project (session_id TEXT, project_id TEXT)`,
		`CREATE TABLE chat_session (session_id TEXT PRIMARY KEY, project_id TEXT, title TEXT)`,
		`CREATE TABLE local_artifact (entry_key TEXT, source_project_id TEXT, user_id TEXT, data TEXT,
			UNIQUE(user_id, source_project_id, entry_key))`,
		// 源账号：P1(路径X)、P2(路径Y)；P1 两个会话 + 产物；P2 一个会话
		`INSERT INTO project VALUES ('p1','projX','X','100')`,
		`INSERT INTO project VALUES ('p2','projY','Y','100')`,
		`INSERT INTO chat_session VALUES ('s-old-1','p1','a'),('s-old-2','p1','b'),('s-old-3','p2','c')`,
		`INSERT INTO session_project VALUES ('s-old-1','p1'),('s-old-2','p1'),('s-old-3','p2')`,
		`INSERT INTO local_artifact VALUES ('k1','p1','100','v')`,
		// 目标账号：已有同路径项目 P1x(路径X) + 一个原生会话
		`INSERT INTO project VALUES ('p1x','projX','X','200')`,
		`INSERT INTO project VALUES ('p2y','projY2','Z','200')`,
		`INSERT INTO chat_session VALUES ('s-native','p1x','native')`,
		`INSERT INTO session_project VALUES ('s-native','p1x')`,
		`INSERT INTO local_artifact VALUES ('k1','p1x','200','dst')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestDoMigrate(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.db")
	makeFixtureDB(t, plain)

	st := DoMigrate(plain, "100", "200")
	if !st.OK {
		t.Fatalf("migrate failed: %s", st.Error)
	}
	if st.Native != 1 || st.Migrated != 3 || st.Retargeted != 1 || st.Reowned != 1 {
		t.Fatalf("stats mismatch: %+v", st)
	}
	if len(st.Mapping) != 3 {
		t.Fatalf("mapping should cover 3 migrated sessions, got %d", len(st.Mapping))
	}

	db, _ := sql.Open("sqlite", plain)
	defer db.Close()
	// 旧 session_id 全部消失
	for _, old := range []string{"s-old-1", "s-old-2", "s-old-3"} {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM chat_session WHERE session_id LIKE ?`, "%"+old+"%").Scan(&n)
		if n != 0 {
			t.Fatalf("old sid %s still present", old)
		}
	}
	// 目标账号现在有 4 个会话（1 原生 + 3 迁入），全挂在 user 200 的项目下
	var cnt int
	_ = db.QueryRow(`
		SELECT COUNT(DISTINCT cs.session_id) FROM chat_session cs
		JOIN session_project sp ON sp.session_id = cs.session_id
		JOIN project p ON p.project_id = sp.project_id WHERE p.user_id='200'`).Scan(&cnt)
	if cnt != 4 {
		t.Fatalf("target sessions = %d, want 4", cnt)
	}
	// 源项目应全部消失（p1 retarget 删除、p2 reown 改 user）
	var residue int
	_ = db.QueryRow(`SELECT COUNT(*) FROM project WHERE user_id='100'`).Scan(&residue)
	if residue != 0 {
		t.Fatalf("source project residue=%d", residue)
	}
	// 目标同键产物保留、源产物删除
	var art int
	_ = db.QueryRow(`SELECT COUNT(*) FROM local_artifact WHERE user_id='100'`).Scan(&art)
	if art != 0 {
		t.Fatalf("source artifact residue=%d", art)
	}

	// 回验
	oldIDs := make([]string, 0, len(st.Mapping))
	for old := range st.Mapping {
		oldIDs = append(oldIDs, old)
	}
	ok, sessions, msg := DoVerify(plain, "200", oldIDs)
	if !ok {
		t.Fatalf("verify failed: %s", msg)
	}
	if sessions != 4 {
		t.Fatalf("verify sessions=%d want 4", sessions)
	}
}

func TestDoMigrateNoProjects(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.db")
	db, _ := sql.Open("sqlite", plain)
	defer db.Close()
	for _, s := range []string{
		`CREATE TABLE project (project_id TEXT PRIMARY KEY, name TEXT, absolute_path TEXT, user_id TEXT)`,
		`CREATE TABLE session_project (session_id TEXT, project_id TEXT)`,
		`CREATE TABLE chat_session (session_id TEXT PRIMARY KEY, project_id TEXT, title TEXT)`,
		`CREATE TABLE local_artifact (entry_key TEXT, source_project_id TEXT, user_id TEXT, data TEXT)`,
		`INSERT INTO project VALUES ('p9','x','X9','999')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	st := DoMigrate(plain, "888", "999")
	if st.OK || st.Error == "" {
		t.Fatalf("expected failure for no-source-projects, got %+v", st)
	}
}
