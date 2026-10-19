package selfupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeExe 写一个能回答 --version 的小脚本（Windows 上跳过，用 e2e 覆盖）。
func fakeExe(t *testing.T, dir, name, version string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in only on unix")
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	return p
}

func TestApplyLocalDryRunLeavesExe(t *testing.T) {
	dir := t.TempDir()
	exe := fakeExe(t, dir, "running", "v1.0.0")
	src := fakeExe(t, dir, "new", "v1.0.1-dev")
	called := false
	u := New(Options{Version: "v1.0.0", Exe: exe, DataDir: dir, Handover: func(context.Context, string) error { called = true; return nil }})
	res, err := u.ApplyLocal(context.Background(), src, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "v1.0.1-dev" || res.SHA256 == "" || !res.DryRun || called {
		t.Fatalf("res=%+v called=%v", res, called)
	}
	if b, _ := os.ReadFile(exe); string(b) != "#!/bin/sh\necho v1.0.0\n" {
		t.Fatal("dry run changed the running exe")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "update", "local-*")); len(m) != 0 {
		t.Fatalf("staged file left behind: %v", m)
	}
}

func TestApplyLocalSwapsAndHandsOver(t *testing.T) {
	dir := t.TempDir()
	exe := fakeExe(t, dir, "running", "v1.0.0")
	src := fakeExe(t, dir, "new", "v1.0.1-dev")
	var got string
	u := New(Options{Version: "v1.0.0", Exe: exe, DataDir: dir, Handover: func(_ context.Context, to string) error { got = to; return nil }})
	if _, err := u.ApplyLocal(context.Background(), src, false); err != nil {
		t.Fatal(err)
	}
	if got != "v1.0.1-dev" {
		t.Fatalf("handover version %q", got)
	}
	if b, _ := os.ReadFile(exe); string(b) != "#!/bin/sh\necho v1.0.1-dev\n" {
		t.Fatal("exe not replaced")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source file must stay where it was")
	}
}

func TestApplyLocalRollsBackOnFailedHandover(t *testing.T) {
	dir := t.TempDir()
	exe := fakeExe(t, dir, "running", "v1.0.0")
	src := fakeExe(t, dir, "new", "v1.0.1-dev")
	u := New(Options{Version: "v1.0.0", Exe: exe, DataDir: dir, Handover: func(context.Context, string) error { return errors.New("health check failed") }})
	if _, err := u.ApplyLocal(context.Background(), src, false); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(exe); string(b) != "#!/bin/sh\necho v1.0.0\n" {
		t.Fatal("running exe not restored")
	}
	if u.Status().State != StateFailed {
		t.Fatalf("state %v", u.Status().State)
	}
}

func TestApplyLocalRejectsBrokenAndSelf(t *testing.T) {
	dir := t.TempDir()
	exe := fakeExe(t, dir, "running", "v1.0.0")
	bad := filepath.Join(dir, "bad")
	_ = os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	u := New(Options{Version: "v1.0.0", Exe: exe, DataDir: dir, Handover: func(context.Context, string) error { t.Fatal("must not hand over"); return nil }})
	if _, err := u.ApplyLocal(context.Background(), bad, false); err == nil {
		t.Fatal("broken exe accepted")
	}
	if _, err := u.ApplyLocal(context.Background(), exe, false); err == nil {
		t.Fatal("swapping to itself accepted")
	}
	if _, err := u.ApplyLocal(context.Background(), filepath.Join(dir, "missing"), true); err == nil {
		t.Fatal("missing file accepted")
	}
}
