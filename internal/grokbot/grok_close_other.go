//go:build !windows

package grokbot

func Close(string) (bool, error) { return false, nil }

func ManagedRunning(string) bool { return false }
