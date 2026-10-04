//go:build windows

package autostart

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"cursor-inner/internal/i18n"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

type Windows struct{}

func New() *Windows { return &Windows{} }

func (w *Windows) Apply(enabled bool, exe string) (State, error) {
	snap := w.snapshot()
	if enabled {
		if err := upsertTask(exe, currentUser()); err != nil {
			if err2 := upsertTask(exe, ""); err2 != nil {
				if ferr := applyFallback(exe); ferr != nil {
					return State{}, i18n.Ef("登录任务：%v；启动项：%v", "Logon task: %v; Run key: %v", err2, ferr)
				}
				return Describe(exe, w.snapshot()), nil
			}
		}
		_ = deleteRun()
		_ = deleteApproved()
		_ = deleteShortcut()
		return Describe(exe, w.snapshot()), nil
	}
	for _, action := range Plan(false, exe, snap) {
		if err := runAction(action); err != nil {
			return State{}, err
		}
	}
	_ = deleteShortcut()
	return Describe(exe, w.snapshot()), nil
}

func (w *Windows) Current(exe string) State {
	return Describe(exe, w.snapshot())
}

func (w *Windows) snapshot() Snapshot {
	snap := Snapshot{Approved: -1}
	if text, ok := queryTask(); ok {
		snap.TaskPresent = true
		snap.TaskCommand, snap.TaskEnabled = ParseTaskXML(text)
	}
	if value, ok := readRun(); ok {
		snap.RunCommand = value
	}
	if flag, ok := readApproved(); ok {
		snap.Approved = flag
	}
	if _, err := os.Stat(shortcutPath()); err == nil {
		snap.Shortcut = true
	}
	return snap
}

func applyFallback(exe string) error {
	_ = deleteTask()
	if err := writeRun(quoteExe(exe)); err != nil {
		return err
	}
	if err := writeApproved(); err != nil {
		return err
	}
	return deleteShortcut()
}

func runAction(action Action) error {
	switch action.Kind {
	case DeleteTask:
		return deleteTask()
	case DeleteRun:
		return deleteRun()
	case DeleteApproved:
		return deleteApproved()
	case DeleteShortcut:
		return deleteShortcut()
	case WriteRun:
		return writeRun(action.Command)
	case WriteApproved:
		return writeApproved()
	default:
		return nil
	}
}

func upsertTask(exe, user string) error {
	file, err := os.CreateTemp("", "cursor-inner-task-*.xml")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(utf16LE(TaskXML(exe, user))); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	out, err := exec.Command("schtasks", "/Create", "/TN", TaskName, "/XML", name, "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(decodeOut(out)))
	}
	return nil
}

func queryTask() (string, bool) {
	out, err := exec.Command("schtasks", "/Query", "/TN", TaskName, "/XML").CombinedOutput()
	if err != nil {
		return "", false
	}
	return decodeOut(out), true
}

func deleteTask() error {
	out, err := exec.Command("schtasks", "/Delete", "/TN", TaskName, "/F").CombinedOutput()
	if err != nil && !missing(decodeOut(out)) {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(decodeOut(out)))
	}
	return nil
}

func missing(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "cannot find") || strings.Contains(text, "找不到") || strings.Contains(lower, "does not exist")
}

func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

func readRun() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(ValueName)
	if err != nil {
		return "", false
	}
	return v, true
}

func writeRun(command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(ValueName, command)
}

func deleteRun() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(ValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func readApproved() (int, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(ValueName)
	if err != nil || len(b) == 0 {
		return 0, false
	}
	return int(b[0]), true
}

func writeApproved() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetBinaryValue(ValueName, EnabledApproved())
}

func deleteApproved() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(ValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func shortcutPath() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`, "cursor-inner.lnk")
}

func deleteShortcut() error {
	err := os.Remove(shortcutPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2+len(units)*2)
	out[0], out[1] = 0xFF, 0xFE
	for i, u := range units {
		out[2+i*2] = byte(u)
		out[3+i*2] = byte(u >> 8)
	}
	return out
}

func decodeOut(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		if len(b)%2 == 1 {
			b = b[:len(b)-1]
		}
		units := make([]uint16, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(units))
	}
	return string(b)
}
