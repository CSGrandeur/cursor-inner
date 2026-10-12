//go:build darwin || linux

package program

import "cursor-inner/internal/console"

// macOS 和 Linux 暂时没有通知区域图标，控制台行为不变。

type residentOptions struct {
	Term  *console.Console
	Open  func(page string)
	Quit  func()
	Feed  func() trayInput
	Quiet func() bool
	Defer bool
}

func prepareResident(residentOptions) bool { return false }
func removeTrayIcon()                      {}
func showTrayIcon()                        {}
func trayKick()                            {}
func trayBalloon(balloon)                  {}
func trayAvailable() bool                  { return false }
func showConsole()                         {}
func hideConsole()                         {}
func concealForExit()                      {}
func runConsoleHolder(int, string)         {}

const traySupported = false
