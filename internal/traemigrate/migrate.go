package traemigrate

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// MigStats 迁移统计。
type MigStats struct {
	OK         bool              `json:"ok"`
	Error      string            `json:"error,omitempty"`
	Native     int               `json:"native"`
	Migrated   int               `json:"migrated"`
	Artifacts  int64             `json:"artifacts"`
	Retargeted int               `json:"retargeted"`
	Reowned    int               `json:"reowned"`
	Mapping    map[string]string `json:"mapping,omitempty"`
}

func openDB(plain string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", plain)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 单连接，避免写锁竞争
	return db, nil
}

func integrityCheck(db *sql.DB) string {
	var res string
	_ = db.QueryRow("PRAGMA integrity_check(3)").Scan(&res)
	return res
}

// sidColumns 找出所有含 session_id 列的表。
func sidColumns(db *sql.DB) ([][2]string, error) {
	var out [][2]string
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name[:7] != "sqlite_" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	for _, t := range tables {
		cols, err := db.Query(fmt.Sprintf(`PRAGMA table_info("%s")`, t))
		if err != nil {
			continue
		}
		for cols.Next() {
			var cid int
			var name, typ string
			var notNull, pk int
			var dflt sql.NullString
			if cols.Scan(&cid, &name, &typ, &notNull, &dflt, &pk) == nil &&
				contains(name, "session_id") {
				out = append(out, [2]string{t, name})
			}
		}
		cols.Close()
	}
	return out, nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// sessionsOf 目标用户经 session_project→project 关联的全部会话。
func sessionsOf(db *sql.DB, userID string) (map[string]bool, error) {
	rows, err := db.Query(`
		SELECT DISTINCT cs.session_id FROM chat_session cs
		JOIN session_project sp ON sp.session_id = cs.session_id
		JOIN project p ON p.project_id = sp.project_id
		WHERE p.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var sid string
		if rows.Scan(&sid) == nil {
			out[sid] = true
		}
	}
	return out, nil
}

// migrateProjects 把 from_user 的项目改挂/转归属到 to_user，本地产物随迁。
func migrateProjects(db *sql.DB, fromUser, toUser string) (retargeted, reowned int, artifacts int64, hasProjects bool, err error) {
	type proj struct{ id, name, path string }
	var src []proj
	rows, err := db.Query(`SELECT project_id, name, absolute_path FROM project WHERE user_id = ?`, fromUser)
	if err != nil {
		return 0, 0, 0, false, err
	}
	for rows.Next() {
		var p proj
		if rows.Scan(&p.id, &p.name, &p.path) == nil {
			src = append(src, p)
		}
	}
	rows.Close()
	if len(src) == 0 {
		return 0, 0, 0, false, nil
	}
	dstByPath := map[string]string{}
	rows, err = db.Query(`SELECT project_id, absolute_path FROM project WHERE user_id = ?`, toUser)
	if err != nil {
		return 0, 0, 0, true, err
	}
	for rows.Next() {
		var pid, apath string
		if rows.Scan(&pid, &apath) == nil {
			dstByPath[apath] = pid
		}
	}
	rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, 0, true, err
	}
	pidMap := map[string]string{}
	for _, p := range src {
		if dst, ok := dstByPath[p.path]; ok {
			// 同路径项目目标账号已存在 → 会话挂到目标项目，删除源项目
			if _, err := tx.Exec(`UPDATE session_project SET project_id = ? WHERE project_id = ?`, dst, p.id); err != nil {
				tx.Rollback()
				return 0, 0, 0, true, err
			}
			if _, err := tx.Exec(`UPDATE chat_session SET project_id = ? WHERE project_id = ?`, dst, p.id); err != nil {
				tx.Rollback()
				return 0, 0, 0, true, err
			}
			pidMap[p.id] = dst
			if _, err := tx.Exec(`DELETE FROM project WHERE project_id = ?`, p.id); err != nil {
				tx.Rollback()
				return 0, 0, 0, true, err
			}
			retargeted++
		} else {
			if _, err := tx.Exec(`UPDATE project SET user_id = ? WHERE project_id = ?`, toUser, p.id); err != nil {
				tx.Rollback()
				return 0, 0, 0, true, err
			}
			reowned++
		}
	}
	for oldPID, newPID := range pidMap {
		if _, err := tx.Exec(`UPDATE local_artifact SET source_project_id = ? WHERE source_project_id = ? AND user_id = ?`,
			newPID, oldPID, fromUser); err != nil {
			tx.Rollback()
			return 0, 0, 0, true, err
		}
	}
	// (user_id, source_project_id, entry_key) 唯一：目标已有同键产物时保留目标行、删源冲突行
	res, err := tx.Exec(`UPDATE local_artifact SET user_id = ? WHERE user_id = ? AND NOT EXISTS (
		SELECT 1 FROM local_artifact t WHERE t.user_id = ?
		AND t.source_project_id = local_artifact.source_project_id
		AND t.entry_key = local_artifact.entry_key)`, toUser, fromUser, toUser)
	if err != nil {
		tx.Rollback()
		return 0, 0, 0, true, err
	}
	artifacts, _ = res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM local_artifact WHERE user_id = ?`, fromUser); err != nil {
		tx.Rollback()
		return 0, 0, 0, true, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, true, err
	}
	return retargeted, reowned, artifacts, true, nil
}

// newObjectID 生成 Trae 风格 object id：4B 大端时间戳 + 8B 随机，hex 24 字符。
func newObjectID(existing map[string]bool) string {
	for {
		b := make([]byte, 12)
		binary.BigEndian.PutUint32(b[:4], uint32(time.Now().Unix()))
		_, _ = rand.Read(b[4:])
		oid := hex.EncodeToString(b)
		if !existing[oid] {
			existing[oid] = true
			return oid
		}
	}
}

// reassignSIDs 迁入会话重分配 session_id（防服务端 4011 会话冲突），全库 LIKE 替换。
func reassignSIDs(db *sql.DB, oldIDs []string) (map[string]string, error) {
	cols, err := sidColumns(db)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	rows, err := db.Query(`SELECT session_id FROM chat_session`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sid string
		if rows.Scan(&sid) == nil {
			existing[sid] = true
		}
	}
	rows.Close()
	mapping := map[string]string{}
	for _, old := range oldIDs {
		mapping[old] = newObjectID(existing)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	for old, newID := range mapping {
		for _, tc := range cols {
			if _, err := tx.Exec(
				fmt.Sprintf(`UPDATE "%s" SET "%s" = replace("%s", ?, ?) WHERE "%s" LIKE ?`, tc[0], tc[1], tc[1], tc[1]),
				old, newID, "%"+old+"%"); err != nil {
				tx.Rollback()
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return mapping, nil
}

// leakCount 旧 session_id 残留计数。
func leakCount(db *sql.DB, oldIDs []string) int {
	cols, _ := sidColumns(db)
	leaks := 0
	for _, old := range oldIDs {
		for _, tc := range cols {
			var n int
			_ = db.QueryRow(
				fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE "%s" LIKE ?`, tc[0], tc[1]),
				"%"+old+"%").Scan(&n)
			leaks += n
		}
	}
	return leaks
}

// DoMigrate 在明文库上执行会话迁移：from_user → to_user。
func DoMigrate(plain, fromUser, toUser string) *MigStats {
	db, err := openDB(plain)
	if err != nil {
		return &MigStats{Error: "打开明文库失败: " + err.Error()}
	}
	defer db.Close()
	if ic := integrityCheck(db); ic != "ok" {
		return &MigStats{Error: "迁移前 integrity=" + ic}
	}
	native, err := sessionsOf(db, toUser)
	if err != nil {
		return &MigStats{Error: "查询目标会话失败: " + err.Error()}
	}
	retargeted, reowned, artifacts, hasProjects, err := migrateProjects(db, fromUser, toUser)
	if err != nil {
		return &MigStats{Error: "项目迁移失败: " + err.Error()}
	}
	if !hasProjects {
		return &MigStats{Error: "当前账号在本地库中没有项目"}
	}
	migratedSet, err := sessionsOf(db, toUser)
	if err != nil {
		return &MigStats{Error: err.Error()}
	}
	var migrated []string
	for sid := range migratedSet {
		if !native[sid] {
			migrated = append(migrated, sid)
		}
	}
	mapping := map[string]string{}
	if len(migrated) > 0 {
		mapping, err = reassignSIDs(db, migrated)
		if err != nil {
			return &MigStats{Error: "重分配 session_id 失败: " + err.Error()}
		}
		oldIDs := make([]string, 0, len(mapping))
		for old := range mapping {
			oldIDs = append(oldIDs, old)
		}
		if leakCount(db, oldIDs) > 0 {
			return &MigStats{Error: "替换后旧 session_id 有残留"}
		}
	}
	// 一致性校验
	var dangling, mismatch, residue, artResidue int
	_ = db.QueryRow(`SELECT COUNT(*) FROM chat_session cs
		LEFT JOIN project p ON p.project_id = cs.project_id
		WHERE p.project_id IS NULL`).Scan(&dangling)
	_ = db.QueryRow(`SELECT COUNT(*) FROM chat_session cs
		JOIN session_project sp ON sp.session_id = cs.session_id
		WHERE cs.project_id != sp.project_id`).Scan(&mismatch)
	_ = db.QueryRow(`SELECT COUNT(*) FROM project WHERE user_id = ?`, fromUser).Scan(&residue)
	_ = db.QueryRow(`SELECT COUNT(*) FROM local_artifact WHERE user_id = ?`, fromUser).Scan(&artResidue)
	if dangling > 0 || mismatch > 0 || residue > 0 || artResidue > 0 {
		return &MigStats{Error: fmt.Sprintf("验证失败 悬空=%d 不一致=%d 源残留=%d/%d", dangling, mismatch, residue, artResidue)}
	}
	for sid := range native {
		var one int
		_ = db.QueryRow(`SELECT 1 FROM chat_session WHERE session_id = ?`, sid).Scan(&one)
		if one != 1 {
			return &MigStats{Error: "目标原生会话缺失 " + sid}
		}
	}
	if ic := integrityCheck(db); ic != "ok" {
		return &MigStats{Error: "迁移后 integrity=" + ic}
	}
	return &MigStats{
		OK: true, Native: len(native), Migrated: len(migrated),
		Artifacts: artifacts, Retargeted: retargeted, Reowned: reowned, Mapping: mapping,
	}
}

// DoVerify 回验：integrity + 旧 id 残留 + 目标会话数。
func DoVerify(plain, toUser string, oldIDs []string) (ok bool, sessions int, errMsg string) {
	db, err := openDB(plain)
	if err != nil {
		return false, 0, "打开回验库失败: " + err.Error()
	}
	defer db.Close()
	if ic := integrityCheck(db); ic != "ok" {
		return false, 0, "回验 integrity=" + ic
	}
	if leaks := leakCount(db, oldIDs); leaks > 0 {
		return false, 0, fmt.Sprintf("回验旧 session_id 残留 %d 处", leaks)
	}
	_ = db.QueryRow(`
		SELECT COUNT(DISTINCT cs.session_id) FROM chat_session cs
		JOIN session_project sp ON sp.session_id = cs.session_id
		JOIN project p ON p.project_id = sp.project_id
		WHERE p.user_id = ?`, toUser).Scan(&sessions)
	return true, sessions, ""
}
