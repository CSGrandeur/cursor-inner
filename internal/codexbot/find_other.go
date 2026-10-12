//go:build !windows

package codexbot

func desktopCandidates() []string { return nil }

func desktopLookups() []func() (string, error) { return nil }
