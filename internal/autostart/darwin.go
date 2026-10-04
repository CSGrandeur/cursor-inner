//go:build darwin

package autostart

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/fsutil"
)

type Darwin struct{}

func New() *Darwin { return &Darwin{} }

func (d *Darwin) Apply(enabled bool, exe string) (State, error) {
	path, err := agentPath()
	if err != nil {
		return State{}, err
	}
	if enabled {
		if err := fsutil.WriteFile(path, []byte(LaunchAgent(exe))); err != nil {
			return State{}, err
		}
		return d.Current(exe), nil
	}
	_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+LaunchAgentLabel).Run()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	return d.Current(exe), nil
}

func (d *Darwin) Current(exe string) State {
	path, err := agentPath()
	if err != nil {
		return State{Mode: "off", Detail: i18n.Of(err)}
	}
	raw, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(raw), "<string>"+xmlText(exe)+"</string>") {
		return State{Enabled: true, Mode: "launch-agent", Detail: i18n.T("已写入登录项（LaunchAgent），登录后启动。", "Registered as a LaunchAgent; starts at login.")}
	}
	return State{Mode: "off", Detail: offDetail}
}

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}
