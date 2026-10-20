//go:build windows

// Package winproctest 让测试在不许创建子进程的作业里跑一段代码，确认它真的没起子进程。
package winproctest

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const env = "CURSOR_INNER_NO_CHILDREN"

// NoChildren 在一个新的测试进程里执行 fn。那个进程先加入“最多 1 个活动进程”的作业，
// 之后 fn 里任何 CreateProcess 都会失败，fn 应把失败当成测试失败。
func NoChildren(t *testing.T, fn func()) {
	t.Helper()
	if os.Getenv(env) == t.Name() {
		if err := confine(); err != nil {
			t.Fatal(err)
		}
		if exec.Command("cmd.exe", "/c", "exit", "0").Run() == nil {
			t.Fatal("the job still lets this process start children")
		}
		fn()
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), env+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func confine() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = 1
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return err
	}
	return windows.AssignProcessToJobObject(job, windows.CurrentProcess())
}
