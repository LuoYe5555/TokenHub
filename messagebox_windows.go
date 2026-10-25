//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// messageBox GUI 模式下的原生错误弹窗（主程序无控制台，stderr 看不到）。
func messageBox(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	mb := user32.NewProc("MessageBoxW")
	t16, _ := syscall.UTF16PtrFromString(title)
	m16, _ := syscall.UTF16PtrFromString(text)
	_, _, _ = mb.Call(0, uintptr(unsafe.Pointer(m16)), uintptr(unsafe.Pointer(t16)), 0x10)
}
