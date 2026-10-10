package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSwapAndRevert(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	staged := filepath.Join(dir, "update", "staged.exe")
	os.MkdirAll(filepath.Dir(staged), 0o755)
	os.WriteFile(exe, []byte("old"), 0o755)
	os.WriteFile(staged, []byte("new"), 0o755)
	backup, err := Swap(exe, staged)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe=%q", b)
	}
	if b, _ := os.ReadFile(backup); string(b) != "old" {
		t.Fatalf("backup=%q", b)
	}
	if err := Revert(exe, backup); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("after revert exe=%q", b)
	}
	if b, _ := os.ReadFile(exe + ".failed"); string(b) != "new" {
		t.Fatalf("failed copy=%q", b)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".failed"); !os.IsNotExist(err) {
		t.Fatal("cleanup left .failed")
	}
}

func TestSwapMissingStagedKeepsOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	if _, err := Swap(exe, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("exe=%q", b)
	}
}
