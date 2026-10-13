//go:build windows

package grokbot

import (
	"testing"

	"cursor-inner/internal/winproc/winproctest"
)

func TestCheckingGrokStartsNoProcess(t *testing.T) {
	winproctest.NoChildren(t, func() {
		if _, err := running(); err != nil {
			t.Fatal(err)
		}
		_, _ = whereGrok()
		_, _ = registryGrok()
	})
}
