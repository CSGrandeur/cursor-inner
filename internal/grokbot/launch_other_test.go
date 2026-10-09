//go:build !windows

package grokbot

import "testing"

func TestLaunchUnavailableOffWindows(t *testing.T) {
	if err := Launch(""); err == nil {
		t.Fatal("expected error")
	}
}
