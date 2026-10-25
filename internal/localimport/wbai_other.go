//go:build !windows

// 非 Windows 平台的占位实现（本机客户端会话导入仅 Windows 有意义）。
package localimport

import (
	"encoding/json"
	"fmt"
)

// 与 wbai.go 中同名类型保持形状一致（仅占位：非 Windows 恒返回空/失败）。
type wbEnvelopeEnvelope struct {
	Suite      int    `json:"suite"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	AuthTag    string `json:"authTag"`
	Ciphertext string `json:"ciphertext"`
}

type wbEncryptedField struct {
	WBEncrypted int    `json:"$wbEncrypted"`
	Envelope    string `json:"envelope"`
}

func wbAsEncrypted(raw json.RawMessage) (wbEncryptedField, bool) { return wbEncryptedField{}, false }
func wbDecodeEnvelope(b64 string) (wbEnvelopeEnvelope, error) {
	return wbEnvelopeEnvelope{}, fmt.Errorf("unsupported")
}
func wbPlainString(raw json.RawMessage) string { return "" }
func wbPlainInt(raw json.RawMessage) int64     { return 0 }
func wbFieldString(raw json.RawMessage, keys map[string][]byte) (string, bool) {
	return "", false
}
func wbFieldStringOr(raw json.RawMessage, keys map[string][]byte, def string) string { return def }
func wbScanProtectorKeys(want []string) map[string][]byte                            { return nil }
