//go:build darwin || linux

package main

func needsClassicConsole() bool { return false }

func startClassicConsole() error { return nil }

func brandConsoleWindow() {}
