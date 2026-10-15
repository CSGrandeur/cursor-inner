//go:build windows

package program

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"cursor-inner/internal/logx"
)

var (
	procIsProcessInJob = kernel32.NewProc("IsProcessInJob")
	procOpenJobObjectW = kernel32.NewProc("OpenJobObjectW")
)

// isProcessInJob 包装 IsProcessInJob（job 为 0：任何作业）。
func isProcessInJob(process windows.Handle) (bool, error) {
	var in int32
	r, _, err := procIsProcessInJob.Call(uintptr(process), 0, uintptr(unsafe.Pointer(&in)))
	if r == 0 {
		return false, err
	}
	return in != 0, nil
}

// spawnError 是一次 CreateProcess 失败：带上程序路径、创建标志和 errno。
// 原来只返回裸的 errno（「Access is denied.」），没法知道是哪个程序、哪组标志失败。
type spawnError struct {
	Path  string
	Via   string // 经 conhost 启动时，真正要运行的程序
	Flags uint32
	Err   error
}

func (e *spawnError) Error() string {
	s := fmt.Sprintf(`CreateProcess "%s"`, e.Path)
	if e.Via != "" {
		s += fmt.Sprintf(` (running "%s")`, e.Via)
	}
	s += fmt.Sprintf(" flags=0x%08x: %v", e.Flags, e.Err)
	var errno syscall.Errno
	if errors.As(e.Err, &errno) {
		s += fmt.Sprintf(" (errno %d)", uint32(errno))
	}
	return s
}

func (e *spawnError) Unwrap() error { return e.Err }

func newSpawnError(argv []string, flags uint32, err error) *spawnError {
	e := &spawnError{Path: argv[0], Flags: flags, Err: err}
	if len(argv) > 1 && strings.EqualFold(filepath.Base(argv[0]), "conhost.exe") {
		e.Via = argv[1]
	}
	return e
}

// deniedBreakaway 判断这次失败是不是「带了 CREATE_BREAKAWAY_FROM_JOB，但所在作业不允许脱离」：
// 作业没有 JOB_OBJECT_LIMIT_BREAKAWAY_OK / SILENT_BREAKAWAY_OK 时，CreateProcess 以 ERROR_ACCESS_DENIED 失败。
func deniedBreakaway(err error, flags uint32) bool {
	var se *spawnError
	return flags&windows.CREATE_BREAKAWAY_FROM_JOB != 0 &&
		errors.As(err, &se) && errors.Is(se.Err, windows.ERROR_ACCESS_DENIED)
}

var warnStayInJob sync.Once

// launchRetry 先按 flags 启动；因作业不允许脱离被拒（ERROR_ACCESS_DENIED）时，去掉 CREATE_BREAKAWAY_FROM_JOB 重试一次。
// 新进程会留在同一个作业里：启动本程序的工具关闭作业（KILL_ON_JOB_CLOSE）时，新旧进程一起结束。
// 这不比以前更糟，旧进程本来就在这个作业里，也会被同样结束；而硬失败会让更新根本无法热交接。
// 两次都失败时，返回的错误包含每一次尝试。
func launchRetry(argv []string, flags uint32, launch func([]string, uint32) error) error {
	err := launch(argv, flags)
	if err == nil || !deniedBreakaway(err, flags) {
		return err
	}
	retryFlags := flags &^ windows.CREATE_BREAKAWAY_FROM_JOB
	err2 := launch(argv, retryFlags)
	if err2 == nil || errors.Is(err2, errLauncherExited) {
		warnStayInJob.Do(func() {
			slog.Warn("本进程在不允许脱离的作业对象里：热交接的新进程会留在同一作业，启动本程序的工具关闭该作业时会一起被结束（旧进程同样会被结束）", "job", queryJob().String())
		})
		return err2
	}
	return errors.Join(err, err2)
}

// jobInfo 是本进程所在作业对象的情况。
type jobInfo struct {
	InJob    bool
	Flags    uint32
	FlagsErr error
}

func (j jobInfo) String() string {
	if !j.InJob {
		return "in_job=false"
	}
	s := "in_job=true"
	if j.FlagsErr != nil {
		return s + " limit_flags=unknown(" + j.FlagsErr.Error() + ")"
	}
	return s + fmt.Sprintf(" limit_flags=0x%x breakaway_ok=%v silent_breakaway_ok=%v kill_on_job_close=%v",
		j.Flags,
		j.Flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0,
		j.Flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0,
		j.Flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0)
}

// canBreakaway 为假表示 CREATE_BREAKAWAY_FROM_JOB 一定会被拒。
func (j jobInfo) canBreakaway() bool {
	if !j.InJob || j.FlagsErr != nil {
		return true
	}
	return j.Flags&(windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK|windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK) != 0
}

// queryJob 查本进程所在的作业。句柄传 0 表示「调用进程所在的作业」。
func queryJob() jobInfo {
	in, err := isProcessInJob(windows.CurrentProcess())
	if err != nil {
		return jobInfo{FlagsErr: err}
	}
	if !in {
		return jobInfo{}
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	err = windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return jobInfo{InJob: true, Flags: info.BasicLimitInformation.LimitFlags, FlagsErr: err}
}

// formatSpawnDiag 是写进 inner.log 的一行：作业情况、要启动的程序、它在不在、多大。
func formatSpawnDiag(j jobInfo, path string, st os.FileInfo, statErr error) string {
	s := j.String() + fmt.Sprintf(` spawn_path="%s"`, path)
	switch {
	case statErr != nil:
		s += fmt.Sprintf(" exists=false stat_error=%q", statErr.Error())
	default:
		s += fmt.Sprintf(" exists=true size=%d", st.Size())
	}
	return s
}

// spawnFailure 是热交接拉不起新进程的错误。Error() 是一行短文本（每次尝试一段），Unwrap 给 errors.Is / errors.As。
type spawnFailure struct {
	err  error
	note string
}

func (f *spawnFailure) Error() string {
	s := strings.ReplaceAll(f.err.Error(), "\n", "；")
	if f.note != "" {
		s += "（" + f.note + "）"
	}
	return s
}

func (f *spawnFailure) Unwrap() error { return f.err }

// failSpawn 把失败时的现场写进 inner.log（只进文件），并返回带每次尝试的短错误。
func failSpawn(exe string, errs []error) error {
	j := queryJob()
	st, statErr := os.Stat(exe)
	slog.Error("热交接拉起新进程失败 "+formatSpawnDiag(j, exe, st, statErr), logx.FileOnly)
	f := &spawnFailure{err: errors.Join(errs...)}
	if !j.canBreakaway() {
		f.note = "本进程在不允许脱离的作业里，" + j.String()
	}
	return f
}
