//go:build darwin || linux

package program

import "cursor-inner/internal/console"

func prepareResident(*console.Console, bool, func(), func()) {}

func removeTrayIcon() {}
