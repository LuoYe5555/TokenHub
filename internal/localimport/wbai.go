//go:build windows

// WorkBuddy 国际版会话解密：新版客户端把 auth .info 文件里的 accessToken/refreshToken
// 加密成 {"$wbEncrypted":1,"envelope":"..."} 信封（AES-256-GCM，suite 1，sym-v1 field framing）。
// 密钥不在磁盘明文存放（keyblob 里也被包裹），但主进程运行时内存中持有原始 32 字节密钥，
// 且 keyId = sha256(key) 前16位 → 用签名扫描即可从运行中的客户端安全取回（仅读自身登录态）。
package localimport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const wbFieldFramingMagic = "WBEV1" // field 级信封的 framing 标记

// wbEnvelopeEnvelope .info 里 envelope 字段解 base64 后的 JSON
type wbEnvelopeEnvelope struct {
	Suite      int    `json:"suite"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	AuthTag    string `json:"authTag"`
	Ciphertext string `json:"ciphertext"`
}

// wbEncryptedField .info 字段值可能出现的加密包装
type wbEncryptedField struct {
	WBEncrypted int    `json:"$wbEncrypted"`
	Envelope    string `json:"envelope"`
}

// wbAAD 复刻 buildAuthenticatedContextAad 的 sym-v1 分支：
// "WB-AAD\0" || 0x01 || lenp(framingMagic) || lenp("sym-v1") || uint32BE(suite)
// || lenp(keyId) || [framing枚举] || 0x00(seq缺省) || 0x00(final缺省)
func wbAAD(keyID string, suite int) []byte {
	lenp := func(b string) []byte {
		out := make([]byte, 4, 4+len(b))
		binary.BigEndian.PutUint32(out, uint32(len(b)))
		return append(out, b...)
	}
	aad := []byte("WB-AAD\x00")
	aad = append(aad, 0x01)
	aad = append(aad, lenp(wbFieldFramingMagic)...)
	aad = append(aad, lenp("sym-v1")...)
	var s [4]byte
	binary.BigEndian.PutUint32(s[:], uint32(suite))
	aad = append(aad, s[:]...)
	aad = append(aad, lenp(keyID)...)
	aad = append(aad, 0x02) // framing 枚举 field=2
	aad = append(aad, 0x00, 0x00)
	return aad
}

// wbOpenEnvelope 用 key32 解一个 field 信封，返回明文字节。
func wbOpenEnvelope(key []byte, envRaw string) ([]byte, error) {
	inner, err := base64Decode(envRaw)
	if err != nil {
		return nil, fmt.Errorf("envelope base64: %w", err)
	}
	var env wbEnvelopeEnvelope
	if err := json.Unmarshal(inner, &env); err != nil {
		return nil, fmt.Errorf("envelope json: %w", err)
	}
	nonce, err := base64Decode(env.Nonce)
	if err != nil || len(nonce) != 12 {
		return nil, fmt.Errorf("nonce 非法")
	}
	tag, err := base64Decode(env.AuthTag)
	if err != nil || len(tag) != 16 {
		return nil, fmt.Errorf("authTag 非法")
	}
	ct, err := base64Decode(env.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("ciphertext base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, append(ct, tag...), wbAAD(env.KeyID, env.Suite))
	if err != nil {
		return nil, fmt.Errorf("GCM open: %w", err)
	}
	return plain, nil
}

func base64Decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty base64")
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// wbCollectKeyIDs 从 .info 文件收集所有加密信封的 keyId + 原始字段。
type wbEncRef struct {
	KeyID    string
	Envelope string
}

func wbEncryptedRefs(infoPath string) []wbEncRef {
	b, err := os.ReadFile(infoPath)
	if err != nil {
		return nil
	}
	var session struct {
		Account map[string]json.RawMessage `json:"account"`
		Auth    map[string]json.RawMessage `json:"auth"`
	}
	if json.Unmarshal(b, &session) != nil {
		return nil
	}
	var refs []wbEncRef
	seen := map[string]bool{}
	add := func(raw json.RawMessage) {
		var f wbEncryptedField
		if json.Unmarshal(raw, &f) != nil || f.WBEncrypted != 1 || f.Envelope == "" {
			return
		}
		inner, err := base64Decode(f.Envelope)
		if err != nil {
			return
		}
		var env wbEnvelopeEnvelope
		if json.Unmarshal(inner, &env) != nil || env.KeyID == "" {
			return
		}
		if !seen[env.KeyID] {
			seen[env.KeyID] = true
			refs = append(refs, wbEncRef{KeyID: env.KeyID})
		}
	}
	for _, raw := range session.Auth {
		add(raw)
	}
	for _, raw := range session.Account {
		add(raw)
	}
	return refs
}

// wbFindKeysInMemory 扫描目标进程内存，返回 sha256(key)[:16] 命中 wantKeyIDs 的密钥表。
// 仅 PROCESS_VM_READ，只读不写；命中全部目标或扫完即止。
func wbFindKeysInMemory(pid uint32, wantKeyIDs []string) map[string][]byte {
	// keyId 是 sha256(key) 的 hex 前 16 位 → 先按原始字节比较前 8 字节（快路径），
	// 命中后再做完整 hex 校验，避免逐位置 Sprintf。
	type wantKey struct {
		id     string
		prefix [8]byte
		done   *atomic.Bool
	}
	var wantList []wantKey
	for _, id := range wantKeyIDs {
		var p [8]byte
		if _, err := hex.Decode(p[:], []byte(id[:16])); err != nil {
			continue
		}
		wantList = append(wantList, wantKey{id: id, prefix: p, done: &atomic.Bool{}})
	}
	if len(wantList) == 0 {
		return nil
	}
	wantAllDone := func() bool {
		for i := range wantList {
			if !wantList[i].done.Load() {
				return false
			}
		}
		return true
	}

	mod := syscall.NewLazyDLL("kernel32.dll")
	pOpen := mod.NewProc("OpenProcess")
	pRPM := mod.NewProc("ReadProcessMemory")
	pVQ := mod.NewProc("VirtualQueryEx")
	h, _, err := pOpen.Call(0x0410, 0, uintptr(pid)) // QUERY_INFORMATION|VM_READ
	if h == 0 {
		_ = err
		return nil
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	found := map[string][]byte{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan []byte, 64)
	stop := make(chan struct{})
	workers := 8
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case buf, ok := <-jobs:
					if !ok {
						return
					}
					for i := 0; i+32 <= len(buf); i++ {
						w := buf[i : i+32]
						sum := sha256.Sum256(w)
						for ki := range wantList {
							p := &wantList[ki].prefix
							if sum[0] == p[0] && sum[1] == p[1] && sum[2] == p[2] && sum[3] == p[3] &&
								sum[4] == p[4] && sum[5] == p[5] && sum[6] == p[6] && sum[7] == p[7] {
								id := hex.EncodeToString(sum[:8])
								if !wantList[ki].done.Swap(true) {
									key := append([]byte(nil), w...)
									mu.Lock()
									found[id] = key
									mu.Unlock()
								}
							}
						}
					}
				}
			}
		}()
	}

	var addr uint64 = 0x10000
	var raw [56]byte
	for addr < 0x7FFFFFFEFFFF {
		if wantAllDone() {
			break
		}
		ret, _, _ := pVQ.Call(uintptr(h), uintptr(addr), uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)))
		if ret == 0 {
			break
		}
		base := *(*uint64)(unsafe.Pointer(&raw[0]))
		size := *(*uint64)(unsafe.Pointer(&raw[24]))
		state := *(*uint32)(unsafe.Pointer(&raw[32]))
		protect := *(*uint32)(unsafe.Pointer(&raw[36]))
		if size == 0 {
			break
		}
		if state == 0x1000 && protect != 0x01 && protect&0x100 == 0 {
			region := make([]byte, size)
			var read uintptr
			if r, _, _ := pRPM.Call(uintptr(h), uintptr(base), uintptr(unsafe.Pointer(&region[0])), uintptr(size), uintptr(unsafe.Pointer(&read))); r != 0 && read > 0 {
				const chunk = 8 << 20
				for off := 0; off < int(read); off += chunk - 31 {
					end := off + chunk
					if end > int(read) {
						end = int(read)
					}
					select {
					case <-stop:
						goto drain
					case jobs <- region[off:end]:
					}
				}
			}
		}
		addr += size
	}
drain:
	close(jobs)
	wg.Wait()
	return found
}

// ── 进程枚举与高层解密入口 ──

// wbProcessIDs 枚举指定映像名的全部进程（Toolhelp32Snapshot）。
func wbProcessIDs(image string) []uint32 {
	const th32csSnapProcess = 0x2
	type procEntry struct {
		Size              uint32
		Usage             uint32
		ProcessID         uint32
		DefaultHeap       uintptr
		ModuleID          uint32
		Threads           uint32
		ParentProcessID   uint32
		PriClassBase      int32
		Flags             uint32
		ExeFile           [260]uint16
	}
	mod := syscall.NewLazyDLL("kernel32.dll")
	pSnap := mod.NewProc("CreateToolhelp32Snapshot")
	pFirst := mod.NewProc("Process32FirstW")
	pNext := mod.NewProc("Process32NextW")
	snap, _, err := pSnap.Call(th32csSnapProcess, 0)
	if snap == uintptr(syscall.InvalidHandle) {
		_ = err
		return nil
	}
	defer syscall.CloseHandle(syscall.Handle(snap))
	var pe procEntry
	pe.Size = uint32(unsafe.Sizeof(pe))
	var out []uint32
	r, _, _ := pFirst.Call(snap, uintptr(unsafe.Pointer(&pe)))
	for r != 0 {
		name := syscall.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(name, image) {
			out = append(out, pe.ProcessID)
		}
		pe.Size = uint32(unsafe.Sizeof(pe))
		r, _, _ = pNext.Call(snap, uintptr(unsafe.Pointer(&pe)))
	}
	return out
}

// wbScanProtectorKeys 扫描本机所有 WorkBuddy 客户端进程，找回 want 里的全部 keyId。
// 密钥只从客户端自身进程读取（等价于客户端自己解密自己的登录态），不做任何写入。
func wbScanProtectorKeys(want []string) map[string][]byte {
	if len(want) == 0 {
		return nil
	}
	result := map[string][]byte{}
	remaining := map[string]bool{}
	for _, id := range want {
		remaining[id] = true
	}
	for _, pid := range wbProcessIDs("WorkBuddy.exe") {
		found := wbFindKeysInMemory(pid, keysOf(remaining))
		for id, key := range found {
			if remaining[id] {
				remaining[id] = false
				result[id] = key
			}
		}
		if len(remaining) == 0 {
			break
		}
	}
	return result
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	return out
}

// ── 字段解析助手（明文/信封双形态） ──

func wbAsEncrypted(raw json.RawMessage) (wbEncryptedField, bool) {
	var f wbEncryptedField
	if len(raw) == 0 || raw[0] != '{' {
		return f, false
	}
	if json.Unmarshal(raw, &f) != nil {
		return f, false
	}
	return f, f.WBEncrypted == 1 && f.Envelope != ""
}

func wbDecodeEnvelope(b64 string) (wbEnvelopeEnvelope, error) {
	inner, err := base64Decode(b64)
	if err != nil {
		return wbEnvelopeEnvelope{}, err
	}
	var env wbEnvelopeEnvelope
	err = json.Unmarshal(inner, &env)
	return env, err
}

func wbPlainString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func wbPlainInt(raw json.RawMessage) int64 {
	var n int64
	if json.Unmarshal(raw, &n) != nil {
		return 0
	}
	return n
}

// wbFieldString 把 auth/account 字段还原成字符串：明文直接返回；
// 加密信封用扫描到的密钥解密。第二个返回值表示该字段是否成功取得。
func wbFieldString(raw json.RawMessage, keys map[string][]byte) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	if raw[0] == '"' {
		return wbPlainString(raw), true
	}
	f, ok := wbAsEncrypted(raw)
	if !ok {
		return "", false
	}
	env, err := wbDecodeEnvelope(f.Envelope)
	if err != nil {
		return "", false
	}
	key := keys[env.KeyID]
	if key == nil {
		return "", false
	}
	plain, err := wbOpenEnvelope(key, f.Envelope)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(plain)), true
}

func wbFieldStringOr(raw json.RawMessage, keys map[string][]byte, def string) string {
	if s, ok := wbFieldString(raw, keys); ok {
		return s
	}
	return def
}
