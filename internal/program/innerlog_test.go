package program

import (
	"os"
	"path/filepath"
	"strings"
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
	if b, _ := os.ReadFile(path); string(b) != "old process: update downloaded\nnew process\nfresh\n" {
		t.Fatalf("normal start must append, log %q", b)
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

// 正常启动也不能清空 inner.log：上一轮的断连记录要能在重启后查到。
func TestOpenInnerLogKeepsPreviousRunOnNormalStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inner.log")
	if err := os.WriteFile(p, []byte("previous run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openInnerLog(p, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("this run\n")
	f.Close()
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "previous run") || !strings.Contains(string(b), "this run") {
		t.Fatalf("inner.log = %q", b)
	}
}
