package antigravitylaunch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindAntigravityUsesFirstExisting(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "Antigravity.exe")
	if err := os.WriteFile(hit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findAntigravity([]string{filepath.Join(dir, "missing.exe"), hit}, func() (string, error) {
		t.Fatal("lookup should not run")
		return "", nil
	})
	if err != nil || got != hit {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindAntigravityResolvesBinShim(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Antigravity.exe")
	shim := filepath.Join(root, "resources", "app", "bin", "antigravity.cmd")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findAntigravity(nil, func() (string, error) { return shim, nil })
	if err != nil || got != exe {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindAntigravityReportsMissing(t *testing.T) {
	if _, err := findAntigravity(nil, func() (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("expected error")
	}
}
