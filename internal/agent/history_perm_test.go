//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewHistoryTightensOldFiles(t *testing.T) {
	dir := t.TempDir()
	conv := filepath.Join(dir, "conversations")
	if err := os.MkdirAll(filepath.Join(conv, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(conv, "a.state")
	blob := filepath.Join(conv, "blobs", "ff")
	for _, p := range []string{old, blob} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(p, 0o644)
	}
	NewHistory(dir)
	for _, p := range []string{old, blob} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(conv); info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", info.Mode().Perm())
	}
}
