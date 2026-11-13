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

func TestRestoreLanguageServerSelfHealsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "resources", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "Antigravity.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lsExe := filepath.Join(bin, lsName)
	lsReal := filepath.Join(bin, lsRealName)
	lsMark := filepath.Join(bin, lsMarkName)
	if err := os.WriteFile(lsExe, []byte("wrapper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lsReal, []byte("official-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lsMark, []byte("mark"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RestoreLanguageServer(exe); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(lsExe)
	if err != nil || string(got) != "official-binary" {
		t.Fatalf("restored binary=%q err=%v", got, err)
	}
	if _, err := os.Stat(lsReal); !os.IsNotExist(err) {
		t.Fatalf(".cursor-inner-real should be removed, err=%v", err)
	}
	if _, err := os.Stat(lsMark); !os.IsNotExist(err) {
		t.Fatalf(".cursor-inner-wrap should be removed, err=%v", err)
	}
	// Second call must be a silent no-op.
	if err := RestoreLanguageServer(exe); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(lsExe)
	if err != nil || string(got) != "official-binary" {
		t.Fatalf("after second call binary=%q err=%v", got, err)
	}
}
