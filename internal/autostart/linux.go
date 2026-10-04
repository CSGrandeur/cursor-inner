//go:build linux

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/fsutil"
)

type Linux struct{}

func New() *Linux { return &Linux{} }

func (l *Linux) Apply(enabled bool, exe string) (State, error) {
	path, err := desktopPath()
	if err != nil {
		return State{}, err
	}
	if enabled {
		if err := fsutil.WriteFile(path, []byte(DesktopEntry(exe))); err != nil {
			return State{}, err
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	return l.Current(exe), nil
}

func (l *Linux) Current(exe string) State {
	path, err := desktopPath()
	if err != nil {
		return State{Mode: "off", Detail: i18n.Of(err)}
	}
	raw, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(raw), "\nExec="+DesktopExec(exe)+"\n") && !strings.Contains(string(raw), "\nHidden=true") {
		return State{Enabled: true, Mode: "xdg-autostart", Detail: i18n.Tf("已写入 %s，登录约 15 秒后启动。", "Written to %s; starts about 15 seconds after login.", path)}
	}
	return State{Mode: "off", Detail: offDetail}
}

func desktopPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "autostart", "cursor-inner.desktop"), nil
}
