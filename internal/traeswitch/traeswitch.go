// Package traeswitch 本机 Trae 客户端登录一键切换。
// 思路参考 TraeHop（MIT）：杀 IDE → 备份 storage.json → 回写目标账号的
// tc 登录信封 + 每账号恒定设备指纹 → 清理登录态缓存 → 重启 IDE。
// 切换前由调用方先 Snapshot 保存当前登录会话到对应账号，保证来回切不丢会话。
package traeswitch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tokenhub/internal/localimport"
	"tokenhub/internal/store"
)

// traeAppNames IDE 进程/数据目录名（与 localimport.traeAppNames 保持一致）。
var traeAppNames = []string{"TRAE SOLO CN", "Trae CN", "TRAE SOLO", "Trae"}

// Snapshot 当前本机 Trae 登录信封。
type Snapshot struct {
	Auth   string // iCubeAuthInfo 信封（tc 加密串）
	Ent    string // iCubeEntitlementInfo 信封
	Server string // iCubeServerData 信封
	UID    string // 当前登录账号 uid（信封可解密时）
	Token  string // 当前 accessToken（信封可解密时）
	Host   string
}

// SnapshotIDE 读取当前 storage.json 的登录信封。未登录时 Auth 为空。
func SnapshotIDE() (*Snapshot, error) {
	dir := localimport.TraeDirForLog()
	if dir == "" {
		return nil, fmt.Errorf("未找到本机 Trae 数据目录")
	}
	b, err := os.ReadFile(filepath.Join(dir, "User", "globalStorage", "storage.json"))
	if err != nil {
		return nil, fmt.Errorf("读取 storage.json 失败: %w", err)
	}
	var storage map[string]any
	if err := json.Unmarshal(b, &storage); err != nil {
		return nil, fmt.Errorf("storage.json 解析失败: %w", err)
	}
	auth, ent, server := localimport.TraeEnvelopes(storage)
	snap := &Snapshot{Auth: auth, Ent: ent, Server: server}
	if a := localimport.TraeEnvelopeInfo(auth); a != nil {
		snap.UID = a.UID
		snap.Token = a.Token
		snap.Host = a.Host
	}
	return snap, nil
}

// Apply 把 acct 的登录信封写进本机 Trae 客户端并重启 IDE。
// 信封缺失时用账号 token 合成兜底信封。返回实际使用的 machineId（已回写 acct）。
func Apply(acct *store.Account) (string, error) {
	dir := localimport.TraeDirForLog()
	if dir == "" {
		return "", fmt.Errorf("未找到本机 Trae 数据目录（未安装 Trae？）")
	}
	storageDir := filepath.Join(dir, "User", "globalStorage")
	storagePath := filepath.Join(storageDir, "storage.json")

	authEnv := acct.ExtraGet(store.ExtraTraeAuthEnvelope)
	if authEnv == "" {
		// 无原始信封 → 用 token 合成（IDE 端可用，但无法自动续期，临期后需重登）
		env, err := localimport.SynthAuthEnvelope(
			acct.AccessToken, acct.RefreshToken, acct.UID,
			acct.DisplayedName(), acct.Email, acct.ExtraGet(store.ExtraTraeAPIHost))
		if err != nil {
			return "", err
		}
		authEnv = env
	}
	entEnv := acct.ExtraGet(store.ExtraTraeEntEnvelope)
	if entEnv == "" {
		env, err := localimport.SynthEntEnvelope()
		if err != nil {
			return "", err
		}
		entEnv = env
	}
	serverEnv := acct.ExtraGet(store.ExtraTraeServerEnvelope)

	killTrae()

	// 每账号恒定设备指纹：避免服务端累积设备数触发上限
	machineID := acct.ExtraGet(store.ExtraTraeMachineID)
	if machineID == "" {
		machineID = uuid()
		acct.ExtraSet(store.ExtraTraeMachineID, machineID)
	}
	_ = os.WriteFile(filepath.Join(dir, "machineid"), []byte(machineID), 0o644)

	// 备份 storage.json（失败不阻断，切换本身可回退）
	if b, err := os.ReadFile(storagePath); err == nil {
		bak := storagePath + ".bak-" + time.Now().Format("20060102150405")
		_ = os.WriteFile(bak, b, 0o644)
	}

	var storage map[string]any
	if b, err := os.ReadFile(storagePath); err == nil && json.Unmarshal(b, &storage) == nil {
	} else {
		storage = map[string]any{}
	}
	storage[localimport.TraeAuthKeyX] = authEnv
	storage[localimport.TraeEntKeyX] = entEnv
	if serverEnv != "" {
		storage[localimport.TraeServerKeyX] = serverEnv
	}
	delete(storage, localimport.TraeUsertagKey)
	// 遥测指纹随 machineId 走（与 TraeHop applyTelemetryIds 一致）
	storage["telemetry.machineId"] = telemetryMachineID(machineID)
	storage["telemetry.sqmId"] = "{" + strings.ToUpper(uuid()) + "}"
	storage["telemetry.devDeviceId"] = uuid()

	nb, _ := json.MarshalIndent(storage, "", "  ")
	if err := os.WriteFile(storagePath, nb, 0o644); err != nil {
		return "", fmt.Errorf("写回 storage.json 失败: %w", err)
	}

	clearLoginCaches(dir)
	launchTrae(dir)
	return machineID, nil
}

// telemetryMachineID 与 TraeHop md5TelemetryId 一致：
// sha256(id)[0:8] || sha256(id+hex(sha256(id)))[0:8] 的 hex。
func telemetryMachineID(id string) string {
	h1 := sha256.Sum256([]byte(id))
	h2 := sha256.Sum256([]byte(id + hex.EncodeToString(h1[:])))
	return hex.EncodeToString(append(append([]byte{}, h1[:8]...), h2[:8]...))
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ── 进程管理 ──

// IDERunning 本机 Trae 是否在运行（供迁移等外部流程判断）。
func IDERunning() bool { return traeRunning() }

// KillIDE 关闭本机 Trae（供迁移等外部流程在改库前调用）。
func KillIDE() { killTrae() }

func traeRunning() bool {
	for _, name := range traeAppNames {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name+".exe", "/NH").Output()
		if err == nil && strings.Contains(string(out), name+".exe") {
			return true
		}
	}
	return false
}

func killTrae() {
	if !traeRunning() {
		return
	}
	for _, name := range traeAppNames {
		_ = exec.Command("taskkill", "/IM", name+".exe").Run()
	}
	time.Sleep(1500 * time.Millisecond)
	if traeRunning() {
		for _, name := range traeAppNames {
			_ = exec.Command("taskkill", "/F", "/IM", name+".exe").Run()
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// launchTrae 启动与数据目录匹配的 IDE（%LOCALAPPDATA%\Programs\<目录>\<目录>.exe）。
func launchTrae(dataDir string) {
	base := filepath.Base(dataDir) // 数据目录名 = 安装目录名
	exe := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", base, base+".exe")
	if _, err := os.Stat(exe); err != nil {
		// 目录名与安装名不一致时逐个兜底
		for _, name := range traeAppNames {
			cand := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", name, name+".exe")
			if _, err := os.Stat(cand); err == nil {
				exe = cand
				break
			}
		}
	}
	if _, err := os.Stat(exe); err != nil {
		return // 找不到 IDE 就不自动拉起，用户手动开
	}
	cmd := exec.Command(exe)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release() // 脱离父进程，独立运行
	}
}

// clearLoginCaches 清理登录态缓存（会话数据 state.vscdb / ModularData 不动）。
func clearLoginCaches(dir string) {
	targets := []string{
		"Cookies", "Cookies-journal",
		filepath.Join("Network", "Cookies"), filepath.Join("Network", "Cookies-journal"),
		"Local State", "Local Storage", "Session Storage", "IndexedDB",
	}
	for _, rel := range targets {
		p := filepath.Join(dir, rel)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			_ = os.RemoveAll(p)
		} else {
			_ = os.Remove(p)
		}
	}
}
