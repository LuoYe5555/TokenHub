//go:build !windows

// 非 Windows 平台：没有「从运行中的客户端进程内存取回解密密钥」的能力，
// 因此加密会话无法解密；明文会话仍可正常读取（见 wbai_common.go）。
package localimport

// wbScanProtectorKeys 非 Windows 平台无法从进程内存找回密钥，返回空表。
func wbScanProtectorKeys(want []string) map[string][]byte { return nil }
