//go:build !windows

package codexbot

func Close(string) (bool, error) { return false, nil }

func ManagedRunning(string) bool { return false }
