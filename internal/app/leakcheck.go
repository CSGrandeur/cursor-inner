package app

import (
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/leakhook"
)

// RunLeakCheck 按需跑一次泄漏检查。只有开发记录版挂了钩子才真正执行；
// 发版包没挂，返回提示，不做任何事。
func (a *App) RunLeakCheck() error {
	if !leakhook.Enabled() {
		return i18n.E("泄漏检查仅在开发记录版可用", "Leak check is only available in the developer build")
	}
	return leakhook.Trigger()
}
