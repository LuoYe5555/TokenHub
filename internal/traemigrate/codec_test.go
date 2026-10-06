package traemigrate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// makeDB 构造一个模拟 SQLite 明文库：页1 带 magic 头，其余页随机内容。
// 每页末尾 80B（ctEnd 后）是 SQLCipher 保留区（IV+HMAC 空间），SQLite 不使用，置零。
func makeDB(t *testing.T, pages int) []byte {
	t.Helper()
	buf := bytes.Repeat([]byte{0}, pages*pageSize)
	copy(buf, sqliteMagic)
	for p := 1; p < pages; p++ {
		off := p * pageSize
		if _, err := rand.Read(buf[off : off+ctEnd]); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟 sqlite 头部关键字段（页大小 4096 大端 @16）
	buf[16], buf[17] = 0x10, 0x00
	return buf
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain.db")
	encPath := filepath.Join(dir, "enc.db")
	decPath := filepath.Join(dir, "dec.db")

	plain := makeDB(t, 5)
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}

	n, err := EncryptDB(plainPath, encPath, key, salt)
	if err != nil || n != 5 {
		t.Fatalf("EncryptDB: n=%d err=%v", n, err)
	}
	// 加密库不应含明文 magic
	enc, _ := os.ReadFile(encPath)
	if bytes.Contains(enc[:100], sqliteMagic) {
		t.Fatal("加密库页1明文区不应出现 SQLite magic（salt 区除外）")
	}

	n2, err := DecryptDB(encPath, decPath, key)
	if err != nil || n2 != 5 {
		t.Fatalf("DecryptDB: n=%d err=%v", n2, err)
	}
	dec, _ := os.ReadFile(decPath)
	if !bytes.Equal(dec, plain) {
		t.Fatal("回环内容不一致")
	}

	// 错误密钥解出的库内容应与原文不同（magic 头总是写入，正确性由 SQL 层 integrity_check 把关）
	wrong := make([]byte, 32)
	_, _ = rand.Read(wrong)
	badPath := filepath.Join(dir, "bad.db")
	if _, err := DecryptDB(encPath, badPath, wrong); err == nil {
		if bad, _ := os.ReadFile(badPath); bytes.Equal(bad, dec) {
			t.Fatal("错误密钥解密结果不应与原文一致")
		}
	}
}

func TestPBKDF2Vector(t *testing.T) {
	// RFC 6070 风格自测：PBKDF2-HMAC-SHA512("password","salt",1,64) 已知值
	got := pbkdf2SHA512([]byte("password"), []byte("salt"), 1, 64)
	want := "867f70cf1ade02cff3752599a3a53dc4af34c7a669815ae5d513554e1c8cf252c02d470a285a0501bad999bfe943c08f050235d7d68b1da55e63f73b60a57fce"
	if hex.EncodeToString(got) != want {
		t.Fatalf("PBKDF2 向量不匹配: got %x", got)
	}
}
