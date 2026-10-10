//go:build !windows

package tunnel

import "cursor-inner/internal/i18n"

func startTunnel(m *Manager) error {
	return i18n.E("当前系统不支持 TUN，仅 Windows 可用", "TUN is only supported on Windows")
}

func stopTunnel(m *Manager) error { return nil }

// DetectDNS 在非 Windows 上不可用，返回空；调用方据此回退。
func DetectDNS() []string { return nil }
