//go:build darwin || linux

package main

import "cursor-inner/internal/console"

func prepareResident(*console.Console, bool, func(), func()) {}

func removeTrayIcon() {}
