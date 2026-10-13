//go:build windows

package codexbot

import (
	"testing"

	"cursor-inner/internal/winproc/winproctest"
)

func TestCheckingCodexStartsNoProcess(t *testing.T) {
	winproctest.NoChildren(t, func() {
		if _, err := running(); err != nil {
			t.Fatal(err)
		}
		for _, lookup := range desktopLookups() {
			_, _ = lookup()
		}
	})
}
