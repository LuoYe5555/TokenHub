// WorkBuddy 会话信封的数据结构（跨平台共享：windows 版解密、其它平台占位实现
// 都要引用这些类型，因此单独放在无构建约束的文件里）。

package localimport

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
