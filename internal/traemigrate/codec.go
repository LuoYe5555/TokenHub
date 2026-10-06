// Package traemigrate Trae AI 会话库迁移引擎（参考 TraeHop MIT 实现，纯 Go 移植）。
// Trae 的 ModularData/ai-agent/database.db 是 SQLCipher 加密 SQLite：
// 4096B 页 = ct[4016] + iv[16] + hmac[64]；页 1 前 16B 为明文 salt；
// 页密文 AES-256-CBC（无填充），页 MAC = HMAC-SHA512(hmacKey, ct||iv||pgnoLE)，
// hmacKey = PBKDF2-SHA512(key, salt^0x3a, 2轮, 32B)。
package traemigrate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// ioReadFull 简化封装。
func ioReadFull(f io.Reader, buf []byte) (int, error) {
	return io.ReadFull(f, buf)
}

const (
	pageSize   = 4096
	ctEnd      = 4016 // 密文体结束偏移（其后 16B IV + 64B HMAC）
	ivOff      = ctEnd
	saltLen    = 16
	macLen     = 64
	walHeader  = 32
	walFrameSz = 24 // 每帧头 24B
)

var sqliteMagic = []byte("SQLite format 3\x00")

// hmacKey 派生页校验密钥：PBKDF2-HMAC-SHA512(key, salt^0x3a, 2, 32)。
func hmacKey(key, salt []byte) []byte {
	hs := make([]byte, saltLen)
	for i := range salt[:saltLen] {
		hs[i] = salt[i] ^ 0x3a
	}
	return pbkdf2SHA512(key, hs, 2, 32)
}

// pbkdf2SHA512 标准 PBKDF2（避免引入 x/crypto 依赖）。
func pbkdf2SHA512(password, salt []byte, rounds, klen int) []byte {
	prf := hmac.New(sha512.New, password)
	hlen := prf.Size()
	numBlocks := (klen + hlen - 1) / hlen
	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hlen)
	U := make([]byte, hlen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:])
		dk = prf.Sum(dk)
		T := dk[(block-1)*hlen : block*hlen]
		copy(U, T)
		for n := 2; n <= rounds; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U)
			for x := range U {
				T[x] ^= U[x]
			}
		}
	}
	return dk[:klen]
}

func newCipher(key []byte) (cipher.Block, error) {
	return aes.NewCipher(key)
}

func decryptPage(dst, src []byte, pgno int, block cipher.Block) error {
	iv := src[ivOff : ivOff+saltLen]
	body := src[:ctEnd]
	if pgno == 1 {
		body = src[saltLen:ctEnd]
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(body))
	mode.CryptBlocks(plain, body)
	for i := range dst {
		dst[i] = 0
	}
	if pgno == 1 {
		copy(dst[0:saltLen], sqliteMagic)
		copy(dst[saltLen:ctEnd], plain)
	} else {
		copy(dst[0:ctEnd], plain)
	}
	// dst[ctEnd:pageSize]（保留区）清零，与 TraeHop out.fill(0) 语义一致
	return nil
}

// DecryptDB 把 SQLCipher 库解密为明文 SQLite，返回页数。
func DecryptDB(src, dst string, key []byte) (int, error) {
	block, err := newCipher(key)
	if err != nil {
		return 0, err
	}
	fin, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer fin.Close()
	fout, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer fout.Close()

	page := make([]byte, pageSize)
	out := make([]byte, pageSize)
	n := 0
	for {
		read, err := ioReadFull(fin, page)
		if err != nil && read == 0 {
			break
		}
		if err != nil || read != pageSize {
			_ = fout.Close()
			return n, errors.New("数据库文件不是完整页（可能损坏）")
		}
		if n == 0 {
			// 校验 salt 存在（页1前16字节明文 salt，非 SQLite 明文头）
			if string(page[:15]) == "SQLite format " {
				_ = fout.Close()
				return 0, errors.New("数据库未加密（不是 SQLCipher 库），无需密钥迁移")
			}
		}
		if err := decryptPage(out, page, n+1, block); err != nil {
			_ = fout.Close()
			return n, err
		}
		if _, err := fout.Write(out); err != nil {
			_ = fout.Close()
			return n, err
		}
		n++
	}
	if n == 0 {
		return 0, errors.New("解密失败：空数据库")
	}
	return n, nil
}

// EncryptDB 把明文 SQLite 加密为 SQLCipher 库（salt 用原库页1前16字节），返回页数。
func EncryptDB(plainPath, outPath string, key, salt []byte) (int, error) {
	block, err := newCipher(key)
	if err != nil {
		return 0, err
	}
	hk := hmacKey(key, salt)
	fin, err := os.Open(plainPath)
	if err != nil {
		return 0, err
	}
	defer fin.Close()
	fout, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer fout.Close()

	page := make([]byte, pageSize)
	pgno := make([]byte, 4)
	mac := make([]byte, 0, macLen)
	n := 0
	for {
		read, err := ioReadFull(fin, page)
		if err != nil && read == 0 {
			break
		}
		if err != nil || read != pageSize {
			_ = fout.Close()
			return n, errors.New("明文库页不完整")
		}
		n++
		iv := make([]byte, saltLen)
		if _, err := rand.Read(iv); err != nil {
			_ = fout.Close()
			return n, err
		}
		mode := cipher.NewCBCEncrypter(block, iv)
		var body []byte
		if n == 1 {
			body = make([]byte, ctEnd-saltLen)
			mode.CryptBlocks(body, page[saltLen:ctEnd])
		} else {
			body = make([]byte, ctEnd)
			mode.CryptBlocks(body, page[:ctEnd])
		}
		binary.LittleEndian.PutUint32(pgno, uint32(n))
		h := hmac.New(sha512.New, hk)
		h.Write(body)
		h.Write(iv)
		h.Write(pgno)
		mac = h.Sum(mac[:0])
		if n == 1 {
			if _, err := fout.Write(salt); err != nil {
				_ = fout.Close()
				return n, err
			}
		}
		if _, err := fout.Write(body); err != nil {
			_ = fout.Close()
			return n, err
		}
		if _, err := fout.Write(iv); err != nil {
			_ = fout.Close()
			return n, err
		}
		if _, err := fout.Write(mac); err != nil {
			_ = fout.Close()
			return n, err
		}
	}
	if n == 0 {
		return 0, errors.New("明文库为空")
	}
	return n, nil
}

// ReadSalt 读原库页1前16字节 salt（重加密要沿用同一 salt）。
func ReadSalt(dbPath string) ([]byte, error) {
	f, err := os.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	salt := make([]byte, saltLen)
	if _, err := f.ReadAt(salt, 0); err != nil && err != io.EOF {
		return nil, err
	}
	return salt, nil
}

// ── WAL 合并：帧页是加密页，帧 checksum 建立在密文上（与 SQLCipher codec 一致）──

// WALApply 把 WAL 里已提交的帧解密后合并进明文库。
func WALApply(plainPath, walPath string, key []byte) (frames, applied, dbsize int, err error) {
	data, err := os.ReadFile(walPath)
	if err != nil || len(data) < walHeader {
		return 0, 0, 0, nil // 无 WAL / 过小：跳过
	}
	magic := be32(data, 0)
	ver := be32(data, 4)
	if (magic != 0x377f0682 && magic != 0x377f0683) || ver != 3007000 {
		return 0, 0, 0, nil
	}
	be := magic&1 == 1
	psz := int(be32(data, 8))
	if psz != pageSize {
		return 0, 0, 0, fmt.Errorf("WAL 页大小 %d 与库 %d 不符", psz, pageSize)
	}
	hs1, hs2 := be32(data, 16), be32(data, 20)
	c1, c2 := be32(data, 24), be32(data, 28)
	ck := func(s0, s1 uint32, buf []byte) (uint32, uint32) {
		for i := 0; i+8 <= len(buf); i += 8 {
			x1, x2 := le32(buf, i), le32(buf, i+4)
			if be {
				x1, x2 = be32(buf, i), be32(buf, i+4)
			}
			s0 = s0 + x1 + s1
			s1 = s1 + x2 + s0
		}
		return s0, s1
	}
	hc1, hc2 := ck(0, 0, data[:24])
	if hc1 != c1 || hc2 != c2 {
		return 0, 0, 0, nil
	}

	type frame struct {
		pgno, commit int
		page         []byte
	}
	var list []frame
	s0, s1 := c1, c2
	frameSize := walFrameSz + psz
	total := (len(data) - walHeader) / frameSize
	off := walHeader
	for i := 0; i < total; i++ {
		fs1, fs2 := be32(data, off+8), be32(data, off+12)
		if fs1 != hs1 || fs2 != hs2 {
			break
		}
		fc1, fc2 := be32(data, off+16), be32(data, off+20)
		pageData := data[off+walFrameSz : off+walFrameSz+psz]
		a0, a1 := ck(s0, s1, data[off:off+8])
		a0, a1 = ck(a0, a1, pageData)
		if a0 != fc1 || a1 != fc2 {
			break
		}
		s0, s1 = a0, a1
		list = append(list, frame{pgno: int(be32(data, off)), commit: int(be32(data, off+4)), page: pageData})
		off += frameSize
	}
	frames = len(list)
	last, dbsz := -1, 0
	for i, f := range list {
		if f.commit > 0 {
			last = i
			dbsz = f.commit
		}
	}
	if last < 0 {
		return frames, 0, 0, nil
	}
	block, err := newCipher(key)
	if err != nil {
		return frames, 0, 0, err
	}
	fd, err := os.OpenFile(plainPath, os.O_RDWR, 0o644)
	if err != nil {
		return frames, 0, 0, err
	}
	defer fd.Close()
	out := make([]byte, pageSize)
	for i := 0; i <= last; i++ {
		f := list[i]
		if err := decryptPage(out, f.page, f.pgno, block); err != nil {
			return frames, i, dbsz, err
		}
		if _, err := fd.WriteAt(out, int64((f.pgno-1)*pageSize)); err != nil {
			return frames, i, dbsz, err
		}
	}
	target := int64(dbsz) * pageSize
	if st, err := fd.Stat(); err == nil {
		if target > st.Size() {
			_, _ = fd.WriteAt(make([]byte, target-st.Size()), st.Size())
		} else if target < st.Size() {
			_ = fd.Truncate(target)
		}
	}
	return frames, last + 1, dbsz, nil
}

func be32(b []byte, off int) uint32 {
	return binary.BigEndian.Uint32(b[off:])
}
func le32(b []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(b[off:])
}
