//go:build windows

package winproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Where 按 where.exe 的规则在当前目录和 PATH 里找 name，返回全部命中的文件。
// 没带扩展名时，原名和加上 PATHEXT 里各个扩展名的都算。
func Where(name string) []string {
	exts := []string{""}
	if filepath.Ext(name) == "" {
		pathext := os.Getenv("PATHEXT")
		if pathext == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		for _, e := range strings.Split(pathext, ";") {
			if e = strings.TrimSpace(e); e != "" {
				exts = append(exts, strings.ToLower(e))
			}
		}
	}
	dirs := []string{"."}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d = strings.Trim(strings.TrimSpace(d), `"`); d != "" {
			dirs = append(dirs, d)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		for _, e := range exts {
			p, err := filepath.Abs(filepath.Join(d, name+e))
			if err != nil || seen[strings.ToLower(p)] {
				continue
			}
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				seen[strings.ToLower(p)] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// Quiet 启动一个不带控制台的子进程，给只在用户操作时才跑一次的 certutil、schtasks 这类命令用。
// 周期性检查不要走这里：CREATE_NO_WINDOW 仍会拉起 conhost，用户看得到黑框一闪。
func Quiet(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd
}
