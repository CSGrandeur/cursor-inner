package program

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenInnerLogKeepsOldLogOnHandover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inner.log")
	if err := os.WriteFile(path, []byte("old process: update downloaded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openInnerLog(path, true)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("new process\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "old process: update downloaded\nnew process\n" {
		t.Fatalf("handover log %q", b)
	}
	f, err = openInnerLog(path, false)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("fresh\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "fresh\n" {
		t.Fatalf("normal start log %q", b)
	}
}
