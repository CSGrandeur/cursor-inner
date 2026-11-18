//go:build darwin

package antigravitylaunch

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// macOS 没有 /proc：ps 列出 pid 与可执行路径，按文件名判定。
func antiProcessesPS() []antiProc {
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil
	}
	var procs []antiProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidStr, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
		if err != nil {
			continue
		}
		path = strings.TrimSpace(path)
		if isAntiExeName(filepath.Base(path)) {
			procs = append(procs, antiProc{pid: pid, exe: path})
		}
	}
	return procs
}
