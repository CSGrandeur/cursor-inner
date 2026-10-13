//go:build windows

package winproc

import (
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Proc 是一个正在运行的进程。读不到的字段留空：提权运行的进程，普通用户读不到路径和命令行。
type Proc struct {
	PID         uint32
	PPID        uint32
	Name        string
	Exe         string
	CommandLine string
}

// List 列出映像名是 names 之一的进程，不区分大小写。
func List(names ...string) ([]Proc, error) {
	all, err := snapshot()
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, p := range all {
		if !nameIn(p.Name, names) {
			continue
		}
		p.Exe, p.CommandLine = details(p.PID)
		out = append(out, p)
	}
	return out, nil
}

func nameIn(name string, names []string) bool {
	for _, n := range names {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

func snapshot() ([]Proc, error) {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []Proc
	for err = windows.Process32First(h, &e); err == nil; err = windows.Process32Next(h, &e) {
		out = append(out, Proc{PID: e.ProcessID, PPID: e.ParentProcessID, Name: windows.UTF16ToString(e.ExeFile[:])})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

func details(pid uint32) (exe, cmdline string) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		exe = windows.UTF16ToString(buf[:n])
	}
	return exe, commandLine(h)
}

// commandLine 用 ProcessCommandLineInformation 读命令行（Windows 8.1 起），只需要 PROCESS_QUERY_LIMITED_INFORMATION。
func commandLine(h windows.Handle) string {
	size := uint32(4096)
	for range 4 {
		buf := make([]byte, size)
		var need uint32
		err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), size, &need)
		if err == nil {
			s := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String()
			runtime.KeepAlive(buf)
			return s
		}
		if (err != windows.STATUS_INFO_LENGTH_MISMATCH && err != windows.STATUS_BUFFER_TOO_SMALL) || need <= size {
			return ""
		}
		size = need
	}
	return ""
}

// KillTree 强制结束这些进程和它们的全部子孙，等同 taskkill /F /T。已经退出的进程不算错。
func KillTree(pids ...uint32) error {
	all, err := snapshot()
	if err != nil {
		return err
	}
	children := map[uint32][]uint32{}
	for _, p := range all {
		if p.PID != p.PPID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	seen := map[uint32]bool{}
	var order []uint32
	queue := append([]uint32(nil), pids...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		order = append(order, pid)
		born, ok := started(pid)
		for _, c := range children[pid] {
			// 父进程早已退出、PID 又被复用时，旧子进程的 PPID 会指向不相干的新进程。
			if cb, cok := started(c); ok && cok && cb >= born {
				queue = append(queue, c)
			}
		}
	}
	var first error
	for _, pid := range order {
		if err := Kill(pid); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Kill 只结束这一个进程，不碰子进程。已经退出的进程不算错。
func Kill(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		if ev, _ := windows.WaitForSingleObject(h, 0); ev == windows.WAIT_OBJECT_0 {
			return nil
		}
		return err
	}
	_, _ = windows.WaitForSingleObject(h, 5000)
	return nil
}

func started(pid uint32) (int64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0, false
	}
	return c.Nanoseconds(), true
}
