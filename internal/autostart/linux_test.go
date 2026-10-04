//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	exe := "/opt/cursor inner/cursor-inner"
	l := New()
	for i := 0; i < 2; i++ {
		st, err := l.Apply(true, exe)
		if err != nil || !st.Enabled {
			t.Fatalf("enable #%d: %+v %v", i, st, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "autostart"))
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}
	if l.Current("/other/cursor-inner").Enabled {
		t.Fatal("entry for another path counted as enabled")
	}
	for i := 0; i < 2; i++ {
		st, err := l.Apply(false, exe)
		if err != nil || st.Enabled {
			t.Fatalf("disable #%d: %+v %v", i, st, err)
		}
	}
}
