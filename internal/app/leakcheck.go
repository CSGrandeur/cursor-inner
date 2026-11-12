package app

import (
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/leakhook"
)

// RunLeakCheck 按需跑一次泄漏检查。检查器经 leakhook 挂入；未挂入时返回提示，不做任何事。
func (a *App) RunLeakCheck() error {
	if !leakhook.Enabled() {
		return i18n.E("此构建未启用泄漏检查", "Leak check is not enabled in this build")
	}
	return leakhook.Trigger()
}
