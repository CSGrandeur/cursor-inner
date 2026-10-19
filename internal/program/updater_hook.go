package program

import "cursor-inner/internal/selfupdate"

// updaterHooks 让外层构建（例如开发用的个人记录版）拿到更新器，
// 在不改本包的前提下挂上额外的更新入口。公开构建不注册任何 hook。
var updaterHooks []func(dataDir string, u *selfupdate.Updater)

// OnUpdater 注册一个 hook，在更新器建好后调用。须在 Main 之前（init 里）注册。
func OnUpdater(f func(dataDir string, u *selfupdate.Updater)) {
	updaterHooks = append(updaterHooks, f)
}
