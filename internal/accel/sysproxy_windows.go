//go:build windows

package accel

// 系统代理一键开关（Windows，PAC 选择性模式）：
//   - 不做全局接管，只写 AutoConfigURL 指向本代理的 /pac 脚本；
//     PAC 里只有加速名单域名走本地代理，其余流量直连或按用户原设置串联
//     —— 国内网站、内网完全不受影响。
//   - 兼容其他加速器：开启前备份 ProxyEnable / ProxyServer / ProxyOverride /
//     AutoConfigURL（Watt Toolkit、Clash 等），并尽力抓取原 PAC 脚本内联串联、
//     原全局代理地址作为 PAC 兜底出口；关闭时全部还原。
//   - 改完用 InternetSetOption 通知系统立即生效（浏览器无需重启）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"

	"tokenhub/internal/logx"
)

const regPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var (
	wininet   = syscall.NewLazyDLL("wininet.dll")
	procISetO = wininet.NewProc("InternetSetOptionW")
)

const (
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
)

func (m *Manager) backupPath() string {
	return filepath.Join(m.cfg.DataDir, "accel_proxy_backup.json")
}

// SystemProxyOn 系统代理当前是否由我们接管。
func (m *Manager) SystemProxyOn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sysProxy
}

// EnableSystemProxy 开启/关闭系统代理 PAC（并通知系统立即生效）。
func (m *Manager) EnableSystemProxy(on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		return m.enableSysProxy()
	}
	return m.disableSysProxy()
}

func openReg() (registry.Key, error) {
	return registry.OpenKey(registry.CURRENT_USER, regPath, registry.QUERY_VALUE|registry.SET_VALUE)
}

func (m *Manager) enableSysProxy() error {
	k, err := openReg()
	if err != nil {
		return err
	}
	defer k.Close()
	// 已是我们的 PAC（上次异常退出残留）：把备份从磁盘捡回来即可
	if cur, _, _ := k.GetStringValue("AutoConfigURL"); cur == pacURL {
		m.restoreBackupFromDisk()
		m.sysProxy = true
		return nil
	}
	b := &proxyBackup{}
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		b.ProxyEnable = uint32(v)
	}
	b.ProxyServer, _, _ = k.GetStringValue("ProxyServer")
	b.ProxyOverride, _, _ = k.GetStringValue("ProxyOverride")
	b.AutoConfigURL, _, _ = k.GetStringValue("AutoConfigURL")
	// 尽力抓原 PAC 脚本内容用于串联（file:// 等抓不了就跳过，PAC 兜底走直连）
	if b.AutoConfigURL != "" && isHTTPURL(b.AutoConfigURL) {
		b.PacScript = fetchPAC(b.AutoConfigURL)
		if b.PacScript == "" {
			logx.Warnf("accel", "原 PAC %s 抓取失败，其余流量将直连（关闭加速后自动还原原 PAC）", b.AutoConfigURL)
		}
	}
	if b.ProxyServer != "" {
		logx.Infof("accel", "检测到已有系统代理 %q（Clash 等），未命中加速名单的流量继续走它", b.ProxyServer)
	}
	if b.AutoConfigURL != "" && b.PacScript == "" {
		logx.Warnf("accel", "检测到已有 PAC %q 且无法串联，开启期间其余流量改为直连，关闭后自动还原", b.AutoConfigURL)
	}
	m.backup = b
	if raw, err := json.MarshalIndent(b, "", "  "); err == nil {
		_ = os.WriteFile(m.backupPath(), raw, 0o600)
	}
	// 写入我们的 PAC；静态全局代理与 PAC 互斥，先清掉（已备份）
	_ = k.SetStringValue("AutoConfigURL", pacURL)
	_ = k.SetDWordValue("ProxyEnable", 0)
	_ = k.DeleteValue("ProxyServer")
	notifySystem()
	m.sysProxy = true
	logx.Infof("accel", "系统代理已切到选择性 PAC: %s（原设置已备份，关闭时还原）", pacURL)
	return nil
}

func (m *Manager) disableSysProxy() error {
	m.sysProxy = false
	k, err := openReg()
	if err != nil {
		return err
	}
	defer k.Close()
	if b := m.loadBackup(); b != nil {
		if b.AutoConfigURL != "" {
			_ = k.SetStringValue("AutoConfigURL", b.AutoConfigURL)
		} else if cur, _, _ := k.GetStringValue("AutoConfigURL"); cur == pacURL {
			_ = k.DeleteValue("AutoConfigURL")
		}
		_ = k.SetDWordValue("ProxyEnable", b.ProxyEnable)
		if b.ProxyServer != "" {
			_ = k.SetStringValue("ProxyServer", b.ProxyServer)
		}
		if b.ProxyOverride != "" {
			_ = k.SetStringValue("ProxyOverride", b.ProxyOverride)
		}
		m.backup = nil
		_ = os.Remove(m.backupPath())
	} else {
		// 没有备份：只清理我们自己写入的痕迹
		if cur, _, _ := k.GetStringValue("AutoConfigURL"); cur == pacURL {
			_ = k.DeleteValue("AutoConfigURL")
		}
	}
	notifySystem()
	logx.Infof("accel", "系统代理已还原为用户原设置")
	return nil
}

// loadBackup 内存优先，磁盘兜底（应对进程崩溃后的残留清理）。
func (m *Manager) loadBackup() *proxyBackup {
	if m.backup != nil {
		return m.backup
	}
	return m.restoreBackupFromDisk()
}

func (m *Manager) restoreBackupFromDisk() *proxyBackup {
	raw, err := os.ReadFile(m.backupPath())
	if err != nil {
		return nil
	}
	var b proxyBackup
	if json.Unmarshal(raw, &b) != nil {
		return nil
	}
	m.backup = &b
	return m.backup
}

// notifySystem 让代理改动立即生效（Explorer/浏览器不用重启）。
func notifySystem() {
	procISetO.Call(0, internetOptionSettingsChanged, 0, 0)
	procISetO.Call(0, internetOptionRefresh, 0, 0)
}
