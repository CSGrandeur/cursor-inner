//go:build windows

package winproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/winproc/winproctest"
)

func TestListReadsPathAndCommandLine(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	procs, err := List(strings.ToUpper(filepath.Base(self)))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.PID != uint32(os.Getpid()) {
			continue
		}
		if !strings.EqualFold(p.Exe, self) {
			t.Fatalf("exe %q, want %q", p.Exe, self)
		}
		if !strings.Contains(p.CommandLine, "-test.") {
			t.Fatalf("command line %q", p.CommandLine)
		}
		return
	}
	t.Fatalf("own process missing from %+v", procs)
}

func TestListWhereAndKillStartNoProcess(t *testing.T) {
	winproctest.NoChildren(t, func() {
		if _, err := List("Grok Bot.exe", "Codex.exe", "ChatGPT.exe"); err != nil {
			t.Fatal(err)
		}
		if len(Where("cmd")) == 0 {
			t.Fatal("cmd not found on PATH")
		}
		if err := KillTree(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWhereFollowsPathext(t *testing.T) {
	want := strings.ToLower(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	for _, name := range []string{"cmd", "cmd.exe", "CMD.EXE"} {
		found := false
		for _, p := range Where(name) {
			if strings.ToLower(p) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("Where(%q) = %q", name, Where(name))
		}
	}
}

// pingTree 起 cmd.exe，它再起 PING.EXE，返回两者的 PID。
func pingTree(t *testing.T) (parent, child uint32) {
	t.Helper()
	cmd := Quiet("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	parent = uint32(cmd.Process.Pid)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		procs, _ := List("PING.EXE")
		for _, p := range procs {
			if p.PPID == parent {
				return parent, p.PID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ping did not start")
	return 0, 0
}

func alive(pid uint32) bool {
	all, _ := snapshot()
	for _, p := range all {
		if p.PID == pid {
			return true
		}
	}
	return false
}

func TestKillTreeEndsDescendants(t *testing.T) {
	parent, child := pingTree(t)
	if err := KillTree(parent); err != nil {
		t.Fatal(err)
	}
	if alive(parent) || alive(child) {
		t.Fatal("tree still running")
	}
}

func TestKillLeavesChildren(t *testing.T) {
	parent, child := pingTree(t)
	defer Kill(child)
	if err := Kill(parent); err != nil {
		t.Fatal(err)
	}
	if alive(parent) {
		t.Fatal("parent still running")
	}
	if !alive(child) {
		t.Fatal("Kill also ended the child")
	}
}

func TestKillIgnoresExitedProcess(t *testing.T) {
	cmd := Quiet("cmd.exe", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := KillTree(uint32(cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
}
