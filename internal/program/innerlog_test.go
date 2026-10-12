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

// 交接时新旧两个进程同时开着 inner.log：旧进程后写的行不能覆盖新进程已经写下的行。
func TestOpenInnerLogConcurrentWritersDoNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inner.log")
	old, err := openInnerLog(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.WriteString("old: handing over\n")
	next, err := openInnerLog(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	next.WriteString("new: started\n")
	old.WriteString("old: handed over\n")
	next.WriteString("new: updated\n")
	want := "old: handing over\nnew: started\nold: handed over\nnew: updated\n"
	if b, _ := os.ReadFile(path); string(b) != want {
		t.Fatalf("got %q", b)
	}
}
