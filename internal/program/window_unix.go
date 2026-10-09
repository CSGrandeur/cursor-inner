//go:build darwin || linux

package program

func needsClassicConsole() bool { return false }

func startClassicConsole() error { return nil }

func brandConsoleWindow() {}
