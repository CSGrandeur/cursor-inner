//go:build windows

package antigravitylaunch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLanguageServerDirFromAntiExe(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "resources", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "Antigravity.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, lsName), []byte("ls"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := languageServerDir(exe)
	if err != nil || dir != bin {
		t.Fatalf("got %q %v", dir, err)
	}
}
