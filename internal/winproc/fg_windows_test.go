//go:build windows

package winproc

import (
	"testing"

	"cursor-inner/internal/winproc/winproctest"
)

func TestForegroundExeStartsNoChildren(t *testing.T) {
	winproctest.NoChildren(t, func() {
		_ = ForegroundExe()
	})
}
