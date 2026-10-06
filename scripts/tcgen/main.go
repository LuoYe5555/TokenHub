// tcgen 生成测试用 Trae storage.json 的 tc 信封密文（仅联调用）。
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

var header = []byte{116, 99, 5, 16, 0, 0}

var woe = []byte{82, 9, 106, 213, 48, 54, 165, 56, 191, 64, 163, 158, 129, 243, 215, 251, 124, 227, 57, 130, 155, 47, 255, 135, 52, 142, 67, 68, 196, 222, 233, 203, 84, 123, 148, 50, 166, 194, 35, 61, 238, 76, 149, 11, 66, 250, 195, 78, 8, 46, 161, 102, 40, 217, 36, 178, 118, 91, 162, 73, 109, 139, 209, 37}
var voe = []byte{31, 221, 168, 51, 136, 7, 199, 49, 177, 18, 16, 89, 39, 128, 236, 95, 96, 81, 127, 169, 25, 181, 74, 13, 45, 229, 122, 159, 147, 201, 156, 239, 160, 224, 59, 77, 174, 42, 245, 176, 200, 235, 187, 60, 131, 83, 153, 97, 23, 43, 4, 126, 186, 119, 214, 38, 225, 105, 20, 99, 85, 33, 12, 125}

func main() {
	auth := map[string]any{
		"token":     "trjwt-local-import",
		"userId":    6688,
		"expiredAt": 1795000000000,
		"host":      "https://api.trae.com.cn",
		"account":   map[string]any{"username": "LocalTrae", "email": "local@trae.test"},
	}
	body, _ := json.Marshal(auth)
	sum := sha512.Sum512(body)
	plain := append(sum[:], body...)
	salt := make([]byte, 32)
	_, _ = rand.Read(salt)
	pepper := make([]byte, len(woe))
	for i := range woe {
		pepper[i] = woe[i] ^ voe[i]
	}
	s1 := sha512.Sum512(salt)
	k := sha512.Sum512(append(s1[:], pepper...))
	block, _ := aes.NewCipher(k[:16])
	pad := 16 - len(plain)%16
	padded := append(append([]byte{}, plain...), make([]byte, pad)...)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	enc := cipher.NewCBCEncrypter(block, k[16:32])
	out := make([]byte, len(padded))
	enc.CryptBlocks(out, padded)
	raw := append(append(append([]byte{}, header...), salt...), out...)
	b64 := base64.StdEncoding.EncodeToString(raw)
	storage := map[string]any{
		"iCubeAuthInfo://icube.cloudide": b64,
		"iCubeAuthInfo://icube-dc:3049374157909753": "1",
	}
	st, _ := json.MarshalIndent(storage, "", "  ")
	if len(os.Args) < 2 {
		fmt.Println(string(st))
		return
	}
	if err := os.MkdirAll(os.Args[1]+"/User/globalStorage", 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[1]+"/User/globalStorage/storage.json", st, 0o600); err != nil {
		panic(err)
	}
	fmt.Println("written:", os.Args[1]+"/User/globalStorage/storage.json")
}
