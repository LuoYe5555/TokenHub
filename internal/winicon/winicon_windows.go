//go:build windows

package winicon

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procPrivateExtractIcons = user32.NewProc("PrivateExtractIconsW")
	procSendMessageW        = user32.NewProc("SendMessageW")
)

const (
	wmSetIcon     = 0x0080
	iconSmall     = 0
	iconBig       = 1
	imageIcon     = 1
	loadFromFile  = 0x00000010
)

// extractIcon 从 ico 文件抽取指定尺寸的 HICON。
func extractIcon(path string, size int) (uintptr, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var hicon uintptr
	var iconID uint32
	n, _, _ := procPrivateExtractIcons.Call(
		uintptr(unsafe.Pointer(p16)),
		0,
		uintptr(size), uintptr(size),
		uintptr(unsafe.Pointer(&hicon)),
		uintptr(unsafe.Pointer(&iconID)),
		1, 0,
	)
	if n == 0 || hicon == 0 {
		return 0, fmt.Errorf("PrivateExtractIcons 失败")
	}
	return hicon, nil
}

// Apply 设置窗口图标：smallIco → 标题栏（ICON_SMALL），bigIco → 任务栏（ICON_BIG）。
func Apply(hwnd unsafe.Pointer, smallIco, bigIco string) error {
	if hwnd == nil {
		return fmt.Errorf("窗口句柄为空")
	}
	big, err := extractIcon(bigIco, 48)
	if err != nil {
		return err
	}
	small, err := extractIcon(smallIco, 16)
	if err != nil {
		small = big
	}
	h := uintptr(hwnd)
	procSendMessageW.Call(h, wmSetIcon, iconBig, big)
	procSendMessageW.Call(h, wmSetIcon, iconSmall, small)
	return nil
}
