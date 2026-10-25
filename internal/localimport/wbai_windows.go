//go:build windows

// WorkBuddy 国际版会话解密（仅 Windows 可用的部分）：
// 密钥不在磁盘明文存放（keyblob 里也被包裹），但主进程运行时内存中持有原始 32 字节密钥，
// 且 keyId = sha256(key) 前16位 → 用签名扫描即可从运行中的客户端安全取回（仅读自身登录态）。
// 信封的解析与解密是纯 Go 的 crypto，已移到 wbai_common.go 供各平台共用。
package localimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

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
		Size            uint32
		Usage           uint32
		ProcessID       uint32
		DefaultHeap     uintptr
		ModuleID        uint32
		Threads         uint32
		ParentProcessID uint32
		PriClassBase    int32
		Flags           uint32
		ExeFile         [260]uint16
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
