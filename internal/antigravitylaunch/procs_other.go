//go:build !windows

package antigravitylaunch

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 非 Windows 的进程查询与结束：读 /proc（Linux）或 ps（macOS），按可执行文件名
// 判定身份，绝不按命令行关键字匹配——命令行里带 antigravity 字样的编辑器、
// 终端不能误杀。只结束主进程（antigravity），不动 language_server 与 wsl 类子进程。

// Running 报告是否有 Antigravity 在运行。
func Running() bool { return len(antiProcesses()) > 0 }

// StopAll 结束全部 Antigravity 主进程：先 SIGTERM 等退出，超时再 SIGKILL。
func StopAll() error {
	procs := antiProcesses()
	if len(procs) == 0 {
		return nil
	}
	for _, p := range procs {
		_ = syscall.Kill(p.pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		left := livingPIDs(procs)
		if len(left) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, pid := range livingPIDs(procs) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// ExePath 返回正在运行的 Antigravity 可执行文件路径；没在运行返回空串。
func ExePath() string {
	for _, p := range antiProcesses() {
		if p.exe != "" {
			return p.exe
		}
	}
	return ""
}

type antiProc struct {
	pid int
	exe string
}

func livingPIDs(procs []antiProc) []int {
	var out []int
	for _, p := range procs {
		if syscall.Kill(p.pid, 0) == nil {
			out = append(out, p.pid)
		}
	}
	return out
}

// isAntiExeName 只认可执行文件名：antigravity（Linux）、Antigravity（macOS app 主二进制）、
// Antigravity Helper 不算（那是渲染/GPU 子进程，主进程退出即跟着退）。
func isAntiExeName(name string) bool {
	lower := strings.ToLower(name)
	return lower == "antigravity" || lower == "antigravity.exe"
}

func antiProcesses() []antiProc {
	if runtime.GOOS == "linux" {
		if entries, err := os.ReadDir("/proc"); err == nil {
			return antiProcessesProc(entries)
		}
	}
	return antiProcessesPS()
}

func antiProcessesProc(entries []os.DirEntry) []antiProc {
	var out []antiProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if isAntiExeName(filepath.Base(exe)) {
			out = append(out, antiProc{pid: pid, exe: exe})
		}
	}
	return out
}
