//go:build !windows

package program

import "cursor-inner/internal/console"

func installConsoleLink(*console.Console, func()) {}
