//go:build !windows

package egress

import "cursor-inner/internal/i18n"

// Start 只在 Windows 上可用。
func Start(enable bool, proxyURL string) error {
	return i18n.E("严格模式只支持 Windows", "Strict mode is only available on Windows")
}
