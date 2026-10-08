package cursorlaunch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindCursorUsesFirstExisting(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "Cursor.exe")
	if err := os.WriteFile(hit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findCursor([]string{filepath.Join(dir, "missing.exe"), hit}, func() (string, error) {
		t.Fatal("lookup should not run")
		return "", nil
	})
	if err != nil || got != hit {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindCursorResolvesBinShim(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Cursor.exe")
	shim := filepath.Join(root, "resources", "app", "bin", "cursor.cmd")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findCursor(nil, func() (string, error) { return shim, nil })
	if err != nil || got != exe {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindCursorReportsMissing(t *testing.T) {
	if _, err := findCursor(nil, func() (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("expected error")
	}
}
