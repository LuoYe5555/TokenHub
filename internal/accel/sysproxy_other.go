//go:build !windows

package accel

import "errors"

// SystemProxyOn 非 Windows 没有统一系统代理实现，恒为 false。
func (m *Manager) SystemProxyOn() bool { return false }

// EnableSystemProxy 非 Windows 暂不支持自动设置系统代理
// （本地加速代理本身仍可手动指向本端口使用）。
func (m *Manager) EnableSystemProxy(bool) error {
	return errors.New("自动系统代理仅支持 Windows，可手动把浏览器代理指向 " + ListenAddr)
}
