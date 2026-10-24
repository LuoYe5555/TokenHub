//go:build !windows

// 非 Windows 平台的占位实现（TokenHub 实际只跑 Windows）。
package localimport

import (
	"encoding/json"
	"fmt"
)

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
