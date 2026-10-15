//go:build windows

package program

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSpawnErrorFormat(t *testing.T) {
	e := newSpawnError([]string{`bin\conhost.exe`, `apps\x.exe`, "--classic-console"},
		classicConsoleCreationFlags(), windows.ERROR_ACCESS_DENIED)
	got := e.Error()
	for _, want := range []string{`CreateProcess "bin\conhost.exe"`, `(running "apps\x.exe")`,
		"flags=0x01000208", "Access is denied.", "(errno 5)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if !errors.Is(e, windows.ERROR_ACCESS_DENIED) {
		t.Error("errors.Is must see the errno")
	}
	if strings.Contains(newSpawnError([]string{`apps\x.exe`}, 0, windows.ERROR_FILE_NOT_FOUND).Error(), "running") {
		t.Error("direct launch has no 'running' part")
	}

	// 两次尝试都失败：短错误一行里列出每一次，errors.Is 仍可用。
	a := newSpawnError([]string{`apps\x.exe`}, classicConsoleCreationFlags(), windows.ERROR_ACCESS_DENIED)
	b := newSpawnError([]string{`apps\x.exe`}, classicConsoleCreationFlags()&^windows.CREATE_BREAKAWAY_FROM_JOB, windows.ERROR_ACCESS_DENIED)
	f := &spawnFailure{err: errors.Join(a, b), note: "n"}
	if strings.Contains(f.Error(), "\n") || strings.Count(f.Error(), "CreateProcess") != 2 || !strings.HasSuffix(f.Error(), "（n）") {
		t.Errorf("unexpected short form: %s", f.Error())
	}
	if !errors.Is(f, windows.ERROR_ACCESS_DENIED) {
		t.Error("errors.Is through spawnFailure")
	}
	if !deniedBreakaway(a, classicConsoleCreationFlags()) || deniedBreakaway(a, 0) ||
		deniedBreakaway(newSpawnError([]string{"x"}, classicConsoleCreationFlags(), windows.ERROR_FILE_NOT_FOUND), classicConsoleCreationFlags()) {
		t.Error("deniedBreakaway only for ACCESS_DENIED with the breakaway flag")
	}
}

func TestSpawnDiagLine(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.exe")
	if err := os.WriteFile(f, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f)
	line := formatSpawnDiag(jobInfo{InJob: true, Flags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}, f, st, err)
	for _, want := range []string{"in_job=true", "limit_flags=0x2000", "breakaway_ok=false", "silent_breakaway_ok=false",
		"kill_on_job_close=true", "exists=true", "size=5"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %s", want, line)
		}
	}
	_, err = os.Stat(filepath.Join(dir, "none.exe"))
	line = formatSpawnDiag(jobInfo{}, "x", nil, err)
	if !strings.Contains(line, "in_job=false") || !strings.Contains(line, "exists=false") || strings.Contains(line, "size=") {
		t.Errorf("missing-file line: %s", line)
	}
	if (jobInfo{InJob: true, Flags: windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK}).canBreakaway() == false ||
		(jobInfo{InJob: true, Flags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}).canBreakaway() ||
		!(jobInfo{}).canBreakaway() {
		t.Error("canBreakaway")
	}
}

// ---- 以下用子进程做真实的作业对象实验 ----

const (
	envHelper = "CI_SPAWN_HELPER"
	envJob    = "CI_SPAWN_JOB"
	envPID    = "CI_SPAWN_PIDFILE"
)

// 被启动的「新进程」：睡一会儿就退出。
func TestSpawnHelperSleep(t *testing.T) {
	if os.Getenv(envHelper) == "" {
		t.Skip("helper")
	}
	time.Sleep(25 * time.Second)
}

func selfTestArgv() []string {
	self, _ := os.Executable()
	return []string{self, "-test.run=^TestSpawnHelperSleep$", "-test.count=1"}
}

func runSelf(t *testing.T, name string, env ...string) []byte {
	t.Helper()
	self, _ := os.Executable()
	cmd := exec.Command(self, "-test.run=^"+name+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// 作业不允许脱离：带 CREATE_BREAKAWAY_FROM_JOB 一定被拒（ERROR_ACCESS_DENIED），去掉后重试成功。
func TestSpawnRetriesWithoutBreakawayInJob(t *testing.T) {
	if os.Getenv("CI_SPAWN_CONFINED") == "" {
		out := runSelf(t, t.Name(), "CI_SPAWN_CONFINED=1", envHelper+"=1")
		t.Logf("%s", out)
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION // LimitFlags 0：没有 BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		t.Skipf("cannot confine this process (nested job not allowed here): %v", err)
	}
	if q := queryJob(); !q.InJob || q.canBreakaway() {
		t.Fatalf("expected confined job, got %s", q)
	}

	// 前提：不重试的直接启动确实以 ACCESS_DENIED 失败。
	argv := selfTestArgv()
	var pids []uint32
	err = launchDetachedPID(argv, classicConsoleCreationFlags(), &pids)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("breakaway into a no-breakaway job should be access denied, got %v", err)
	}
	t.Logf("premise confirmed: %v", err)

	// 修复：launchRetry 重试成功，且错误路径不会误报。
	launched := 0
	err = launchRetry(argv, classicConsoleCreationFlags(), func(a []string, f uint32) error {
		launched++
		return launchDetachedPID(a, f, &pids)
	})
	if err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}
	if launched != 2 {
		t.Fatalf("expected breakaway attempt then retry, got %d launches", launched)
	}
	for _, pid := range pids {
		killPID(pid)
	}

	// 两次都失败：错误里有两次尝试。
	argv2 := []string{filepath.Join(t.TempDir(), "nope.exe")}
	err = launchRetry(argv2, classicConsoleCreationFlags(), launchDetached)
	if err == nil {
		t.Fatal("missing exe must fail")
	}
	// 文件不存在不是 ACCESS_DENIED，不应重试：只有一次尝试。
	if strings.Count(err.Error(), "CreateProcess") != 1 {
		t.Fatalf("not-found must not retry: %v", err)
	}
}

// launchDetachedPID 与 launchDetached 相同，但把新进程 pid 记下来，测试结束时好清理。
func launchDetachedPID(argv []string, flags uint32, pids *[]uint32) error {
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return err
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, flags, nil, nil, &si, &pi); err != nil {
		return newSpawnError(argv, flags, err)
	}
	*pids = append(*pids, pi.ProcessId)
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}

func killPID(pid uint32) {
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
		windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

func pidAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259
}

// 中间进程：进到外面建的、带 KILL_ON_JOB_CLOSE 且不允许脱离的作业里，再用 launchRetry 起新进程，把 pid 写到文件。
func TestSpawnMiddle(t *testing.T) {
	name := os.Getenv(envJob)
	if name == "" {
		t.Skip("helper")
	}
	p, _ := windows.UTF16PtrFromString(name)
	const assignProcess, query = 0x1, 0x4
	r, _, e := procOpenJobObjectW.Call(assignProcess|query, 0, uintptr(unsafe.Pointer(p)))
	if r == 0 {
		t.Fatal(e)
	}
	job := windows.Handle(r)
	err := windows.AssignProcessToJobObject(job, windows.CurrentProcess())
	windows.CloseHandle(job) // 不能留着句柄，否则外面关了作业也不会触发 KILL_ON_JOB_CLOSE
	if err != nil {
		t.Fatal(err)
	}
	os.Setenv(envHelper, "1")
	var pids []uint32
	if err := launchRetry(selfTestArgv(), classicConsoleCreationFlags(), func(a []string, f uint32) error {
		return launchDetachedPID(a, f, &pids)
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(envPID), []byte(strconv.Itoa(int(pids[len(pids)-1]))), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Second)
}

// 回答「新进程会不会随作业一起结束」：会。热交接的新进程留在启动者的作业里，作业被关闭（KILL_ON_JOB_CLOSE）时一并结束。
func TestSpawnedChildEndsWithJobClose(t *testing.T) {
	if os.Getenv(envJob) != "" {
		t.Skip("inner")
	}
	name := `Local\ci-spawn-` + strconv.Itoa(os.Getpid())
	p, _ := windows.UTF16PtrFromString(name)
	job, err := windows.CreateJobObject(nil, p)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			windows.CloseHandle(job)
		}
	}()
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	self, _ := os.Executable()
	mid := exec.Command(self, "-test.run=^TestSpawnMiddle$", "-test.count=1")
	mid.Env = append(os.Environ(), envJob+"="+name, envPID+"="+pidFile)
	if err := mid.Start(); err != nil {
		t.Fatal(err)
	}
	var child uint32
	for i := 0; i < 100 && child == 0; i++ {
		if raw, err := os.ReadFile(pidFile); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			child = uint32(n)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if child == 0 {
		mid.Process.Kill()
		t.Fatal("middle process did not report a child pid")
	}
	defer killPID(child)
	if !pidAlive(child) {
		t.Fatal("child should be alive while the job is open")
	}
	closed = true
	windows.CloseHandle(job) // 启动者关闭作业
	dead := false
	for i := 0; i < 50; i++ {
		if !pidAlive(child) {
			dead = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = mid.Wait()
	if !dead {
		t.Fatal("child survived the job close")
	}
	t.Log("child (spawned without breakaway) was killed when the launcher closed the KILL_ON_JOB_CLOSE job")
}
