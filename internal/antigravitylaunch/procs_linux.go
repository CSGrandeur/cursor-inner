//go:build linux

package antigravitylaunch

import (
	"os/exec"
	"strconv"
	"strings"
)

// ps 兜底（/proc 不可读时，如容器内 hidepid）：只认 comm 名，不看命令行。
func antiProcessesPS() []antiProc {
	out, err := exec.Command("ps", "-eo", "pid=,comm=").Output()
	if err != nil {
		return nil
	}
	var procs []antiProc
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || !isAntiExeName(fields[1]) {
			continue
		}
		procs = append(procs, antiProc{pid: pid})
	}
	return procs
}
