package traemigrate

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tokenhub/internal/localimport"
	"tokenhub/internal/store"
	"tokenhub/internal/traeswitch"
)

// DBKeyPath 数据库密钥存储路径（data/trae-dbkey.txt，64 位 hex，面板设置）。
func DBKeyPath(dataDir string) string { return filepath.Join(dataDir, "trae-dbkey.txt") }

// SaveDBKey 保存数据库密钥（64 hex）。
func SaveDBKey(dataDir, key string) error {
	key = strings.TrimSpace(strings.ToLower(key))
	if !validKey(key) {
		return fmt.Errorf("密钥格式错误：需要 64 位 hex（32 字节 SQLCipher 密钥）")
	}
	return os.WriteFile(DBKeyPath(dataDir), []byte(key), 0o600)
}

// LoadDBKey 读取已存密钥；未配置返回空串。
func LoadDBKey(dataDir string) string {
	b, err := os.ReadFile(DBKeyPath(dataDir))
	if err != nil {
		return ""
	}
	k := strings.TrimSpace(string(b))
	if validKey(k) {
		return k
	}
	return ""
}

func validKey(k string) bool {
	if len(k) != 64 {
		return false
	}
	_, err := hex.DecodeString(k)
	return err == nil
}

// Result 迁移结果。
type Result struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	FromUser string `json:"fromUser"`
	ToUser   string `json:"toUser"`
	Native   int    `json:"native"`
	Migrated int    `json:"migrated"`
	Backups  string `json:"backup,omitempty"`
}

// Job 一次迁移任务的状态（面板轮询）。
type Job struct {
	mu      sync.Mutex
	running bool
	logs    []string
	result  *Result
}

var globalJob Job

// JobStatus 当前任务状态。
func JobStatus() (running bool, logs []string, result *Result) {
	globalJob.mu.Lock()
	defer globalJob.mu.Unlock()
	return globalJob.running, append([]string{}, globalJob.logs...), globalJob.result
}

func (j *Job) start() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	j.running, j.logs, j.result = true, nil, nil
	return true
}
func (j *Job) logf(format string, args ...any) {
	j.mu.Lock()
	j.logs = append(j.logs, fmt.Sprintf(format, args...))
	j.mu.Unlock()
}
func (j *Job) finish(res *Result) {
	j.mu.Lock()
	j.running, j.result = false, res
	j.mu.Unlock()
}

// StartMigrate 启动一次会话迁移（异步；进度用 JobStatus 轮询）。
// 流程与 TraeHop 一致：解密 → 合并 WAL → 迁移 SQL → 重加密 → 回验 → 写回 → 切号重启。
func StartMigrate(target *store.Account, dataDir, keyHex string) error {
	if !globalJob.start() {
		return fmt.Errorf("已有迁移任务在进行中")
	}
	go func() {
		res := run(target, dataDir, keyHex)
		globalJob.finish(res)
	}()
	return nil
}

func run(target *store.Account, dataDir, keyHex string) *Result {
	res := &Result{ToUser: target.UID}
	log := globalJob.logf

	key, err := keyBytes(keyHex, dataDir)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	// 定位 Trae 数据目录与 AI 库
	dir := localimport.TraeDirForLog()
	if dir == "" {
		res.Error = "未找到本机 Trae 数据目录"
		return res
	}
	db := filepath.Join(dir, "ModularData", "ai-agent", "database.db")
	if st, err := os.Stat(db); err != nil || st.Size() < pageSize {
		res.Error = "未找到 Trae AI 数据库，请先启动一次 Trae 客户端"
		return res
	}
	// 当前登录 = 来源账号
	snap, err := traeswitch.SnapshotIDE()
	if err != nil {
		res.Error = "读取本机 Trae 登录失败: " + err.Error()
		return res
	}
	if snap.UID == "" {
		res.Error = "本机 Trae 未登录（无法确定来源账号）"
		return res
	}
	if snap.UID == target.UID {
		res.Error = "目标账号就是当前登录账号，无需迁移"
		return res
	}
	res.FromUser = snap.UID
	log("当前账号 %s → 目标账号 %s（%s）", snap.UID, target.UID, target.DisplayedName())

	// 杀 IDE（保证 WAL 完整、库不被占用）
	if traeswitch.IDERunning() {
		log("关闭 Trae IDE…")
		traeswitch.KillIDE()
	}

	// 备份
	stamp := time.Now().Format("20060102150405")
	bak := db + ".bak-" + stamp
	if err := copyFile(db, bak); err != nil {
		res.Error = "备份失败: " + err.Error()
		return res
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + ext); err == nil {
			_ = copyFile(db+ext, bak+ext)
		}
	}
	res.Backups = bak
	log("已备份原库 → " + filepath.Base(bak))

	// 解密
	plain := bak + ".plain"
	defer os.Remove(plain)
	log("解密数据库…")
	if _, err := DecryptDB(db, plain, key); err != nil {
		res.Error = "解密失败: " + err.Error()
		return res
	}
	// WAL 合并
	wal := db + "-wal"
	if st, err := os.Stat(wal); err == nil && st.Size() > 32 {
		log("合并 WAL 日志…")
		frames, applied, dbsize, err := WALApply(plain, wal, key)
		if err != nil {
			res.Error = fmt.Sprintf("WAL 合并失败: %v（请正常退出 Trae 后重试）", err)
			return res
		}
		log("  帧 %d，应用 %d，库 %d 页", frames, applied, dbsize)
	}

	// 迁移 SQL
	log("迁移项目与会话…")
	m := DoMigrate(plain, snap.UID, target.UID)
	if !m.OK {
		res.Error = "迁移失败: " + m.Error
		return res
	}
	log("  原生 %d / 迁入 %d 会话，产物 %d 条", m.Native, m.Migrated, m.Artifacts)

	// 重加密（沿用原库 salt）
	salt, err := ReadSalt(bak)
	if err != nil {
		res.Error = "读取 salt 失败: " + err.Error()
		return res
	}
	enc := bak + ".enc"
	defer os.Remove(enc)
	log("重加密…")
	n2, err := EncryptDB(plain, enc, key, salt)
	if err != nil || n2 < 1 {
		res.Error = "重加密失败: " + fmt.Sprint(n2, err)
		return res
	}
	log("  %d 页", n2)

	// 回验
	verifyDb := bak + ".verify"
	oldIDs := make([]string, 0, len(m.Mapping))
	for old := range m.Mapping {
		oldIDs = append(oldIDs, old)
	}
	if _, err := DecryptDB(enc, verifyDb, key); err != nil {
		res.Error = "回验解密失败: " + err.Error()
		return res
	}
	ok, sessions, errMsg := DoVerify(verifyDb, target.UID, oldIDs)
	os.Remove(verifyDb)
	if !ok {
		res.Error = errMsg
		return res
	}
	log("  回验通过，目标账号共 %d 会话", sessions)

	// 写回主库，清掉 wal/shm
	if err := copyFile(enc, db); err != nil {
		res.Error = "写回主库失败: " + err.Error()
		return res
	}
	for _, ext := range []string{"-wal", "-shm"} {
		os.Remove(db + ext)
	}
	res.OK, res.Native, res.Migrated = true, m.Native, m.Migrated
	log("已写回主库")

	// 切换到目标账号并重启 IDE（会话随库归属目标账号）
	log("切换到目标账号并重启 Trae…")
	if _, err := traeswitch.Apply(target); err != nil {
		log("  切换登录失败（库已迁移完成，可手动「切入Trae」）: %v", err)
	}
	return res
}

func keyBytes(keyHex, dataDir string) ([]byte, error) {
	if keyHex == "" {
		keyHex = LoadDBKey(dataDir)
	}
	keyHex = strings.TrimSpace(strings.ToLower(keyHex))
	if !validKey(keyHex) {
		return nil, fmt.Errorf("数据库密钥未配置或格式错误（面板设置中填入 64 位 hex）")
	}
	b, _ := hex.DecodeString(keyHex)
	// 校验派生逻辑可用性（等价 sha512 引用，避免未使用告警）
	_ = sha512.Sum512(b)
	return b, nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
