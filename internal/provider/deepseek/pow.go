// DeepSeek 网页版 Proof-of-Work：DeepSeekHashV1 求解器（纯 Go，多核并行）。
//
// 算法（逆向自官方 wasm sha3_wasm_bg.wasm，社区公开实现一致验证）：
// DeepSeekHashV1 = SHA3-256 但 Keccak-f[1600] 跳过第 0 轮（只做 rounds 1..23）。
// 其余参数不变：rate=136，padding 0x06+0x80，输出 32 字节。
// 协议：服务端选 answer ∈ [0, difficulty)，challenge = hash(salt_expireAt_answer)；
// 客户端遍历 [0, difficulty) 找到匹配 nonce，把答案放进 x-ds-pow-response 头。
//
// 参考实现：go-deepseek-web (MIT) / deepseek-free-api，测试向量实测一致。
package deepseek

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const sha3Rate = 136

// keccakRC 标准 Keccak-f[1600] 轮常数（24 轮；跳过第 0 轮即从下标 1 开始）。
var keccakRC = []uint64{
	0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000,
	0x000000000000808B, 0x0000000080000001, 0x8000000080008081, 0x8000000000008009,
	0x000000000000008A, 0x0000000000000088, 0x0000000080008009, 0x000000008000000A,
	0x000000008000808B, 0x800000000000008B, 0x8000000000008089, 0x8000000000008003,
	0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
	0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
}

var rotTable = [5][5]int{
	{0, 36, 3, 41, 18},
	{1, 44, 10, 45, 2},
	{62, 6, 43, 15, 61},
	{28, 55, 25, 21, 56},
	{27, 20, 39, 8, 14},
}

type rhoPiEntry struct {
	src, dst, rot int
}

var rhoPiTable [25]rhoPiEntry

func init() {
	idx := 0
	for x := 0; x < 5; x++ {
		for y := 0; y < 5; y++ {
			rhoPiTable[idx] = rhoPiEntry{
				src: x + 5*y,
				dst: y + 5*((2*x+3*y)%5),
				rot: rotTable[x][y],
			}
			idx++
		}
	}
}

// keccakF 对 state 跑给定的轮常数序列（DeepSeek 变体传 keccakRC[1:]，跳过第 0 轮）。
func keccakF(A *[25]uint64, roundConstants []uint64) {
	var C, D [5]uint64
	var B [25]uint64
	for _, rc := range roundConstants {
		// Theta
		for x := 0; x < 5; x++ {
			C[x] = A[x] ^ A[x+5] ^ A[x+10] ^ A[x+15] ^ A[x+20]
		}
		for x := 0; x < 5; x++ {
			D[x] = C[(x+4)%5] ^ rotl(C[(x+1)%5], 1)
		}
		for i := 0; i < 25; i++ {
			A[i] ^= D[i%5]
		}
		// Rho & Pi
		for _, e := range rhoPiTable {
			v := A[e.src]
			if e.rot != 0 {
				v = rotl(v, e.rot)
			}
			B[e.dst] = v
		}
		// Chi
		for y := 0; y < 5; y++ {
			row := y * 5
			for x := 0; x < 5; x++ {
				A[row+x] = B[row+x] ^ ((^B[row+(x+1)%5]) & B[row+(x+2)%5])
			}
		}
		// Iota
		A[0] ^= rc
	}
}

func rotl(v uint64, n int) uint64 {
	return v<<uint(n) | v>>uint(64-n)
}

// DeepSeekHashV1 计算 SHA3-256 padding + Keccak-f rounds 1..23（单块，输入 < 136 字节）。
func DeepSeekHashV1(data []byte) [32]byte {
	var block [sha3Rate]byte
	copy(block[:], data)
	block[len(data)] = 0x06
	block[sha3Rate-1] |= 0x80

	var A [25]uint64
	for i := 0; i < 17; i++ {
		A[i] = binary.LittleEndian.Uint64(block[i*8 : (i+1)*8])
	}
	keccakF(&A, keccakRC[1:]) // 关键差异：跳过第 0 轮

	var out [32]byte
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(out[i*8:(i+1)*8], A[i])
	}
	return out
}

// Challenge /api/v0/chat/create_pow_challenge 返回的 biz_data.challenge。
type Challenge struct {
	Algorithm  string `json:"algorithm"`
	Challenge  string `json:"challenge"`
	Salt       string `json:"salt"`
	Difficulty int64  `json:"difficulty"`
	ExpireAt   int64  `json:"expire_at"`
	Signature  string `json:"signature"`
	TargetPath string `json:"target_path"`
}

// solveNonce 多核并行搜索匹配 nonce；找不到返回错误（difficulty 耗尽=参数错或已过期）。
func solveNonce(ch Challenge) (int64, error) {
	if ch.Algorithm != "" && ch.Algorithm != "DeepSeekHashV1" {
		return 0, fmt.Errorf("不支持的 PoW 算法: %s", ch.Algorithm)
	}
	target := strings.ToLower(ch.Challenge)
	if len(target) != 64 {
		return 0, fmt.Errorf("challenge 长度异常: %d", len(target))
	}
	var want [32]byte
	if _, err := hex.Decode(want[:], []byte(target)); err != nil {
		return 0, fmt.Errorf("challenge 不是合法 hex: %v", err)
	}
	prefix := fmt.Sprintf("%s_%d_", ch.Salt, ch.ExpireAt)
	prefixBytes := []byte(prefix)

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	diff := ch.Difficulty
	if diff < 1 {
		diff = 144000
	}
	chunk := (diff + int64(workers) - 1) / int64(workers)

	var found atomic.Bool
	var answer atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		start := int64(w) * chunk
		end := start + chunk
		if end > diff {
			end = diff
		}
		if start >= diff {
			break
		}
		wg.Add(1)
		go func(s, e int64) {
			defer wg.Done()
			buf := make([]byte, len(prefixBytes)+24)
			copy(buf, prefixBytes)
			pLen := len(prefixBytes)
			for nonce := s; nonce < e; nonce++ {
				if found.Load() {
					return
				}
				ns := strconv.FormatInt(nonce, 10)
				copy(buf[pLen:], ns)
				if DeepSeekHashV1(buf[:pLen+len(ns)]) == want {
					if found.CompareAndSwap(false, true) {
						answer.Store(nonce)
					}
					return
				}
			}
		}(start, end)
	}
	wg.Wait()
	if !found.Load() {
		return 0, fmt.Errorf("PoW 无解（difficulty=%d 耗尽，challenge 无效或已过期）", diff)
	}
	return answer.Load(), nil
}

// SolvePow 领到的 challenge 求解并组 x-ds-pow-response 头值（紧凑 JSON 的 base64）。
func SolvePow(ch Challenge) (string, error) {
	nonce, err := solveNonce(ch)
	if err != nil {
		return "", err
	}
	if ch.TargetPath == "" {
		ch.TargetPath = "/api/v0/chat/completion"
	}
	if ch.Algorithm == "" {
		ch.Algorithm = "DeepSeekHashV1"
	}
	sol := map[string]any{
		"algorithm":   ch.Algorithm,
		"challenge":   ch.Challenge,
		"salt":        ch.Salt,
		"answer":      nonce,
		"signature":   ch.Signature,
		"target_path": ch.TargetPath,
	}
	b, err := json.Marshal(sol)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
