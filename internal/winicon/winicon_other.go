//go:build !windows

package winicon

import "unsafe"

// Apply 非 Windows 平台无操作。
func Apply(hwnd unsafe.Pointer, smallIco, bigIco string) error { return nil }
