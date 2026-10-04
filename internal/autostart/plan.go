package autostart

import (
	"strings"

	"cursor-inner/internal/i18n"
)

const (
	TaskName  = `cursor-inner`
	ValueName = `cursor-inner`
)

type Snapshot struct {
	TaskPresent bool
	TaskEnabled bool
	TaskCommand string
	RunCommand  string
	Approved    int
	Shortcut    bool
}

type Kind int

const (
	UpsertTask Kind = iota
	DeleteTask
	WriteRun
	DeleteRun
	WriteApproved
	DeleteApproved
	DeleteShortcut
)

type Action struct {
	Kind    Kind
	Command string
}

func EnabledApproved() []byte {
	return []byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

func Plan(enabled bool, exe string, snap Snapshot) []Action {
	if enabled {
		var actions []Action
		actions = append(actions, Action{Kind: UpsertTask, Command: exe})
		if snap.RunCommand != "" {
			actions = append(actions, Action{Kind: DeleteRun})
		}
		if snap.Approved == 2 || snap.Approved == 3 {
			actions = append(actions, Action{Kind: DeleteApproved})
		}
		if snap.Shortcut {
			actions = append(actions, Action{Kind: DeleteShortcut})
		}
		return actions
	}
	var actions []Action
	if snap.TaskPresent {
		actions = append(actions, Action{Kind: DeleteTask})
	}
	if snap.RunCommand != "" {
		actions = append(actions, Action{Kind: DeleteRun})
	}
	if snap.Approved == 2 || snap.Approved == 3 {
		actions = append(actions, Action{Kind: DeleteApproved})
	}
	if snap.Shortcut {
		actions = append(actions, Action{Kind: DeleteShortcut})
	}
	return actions
}

func Fallback(exe string) []Action {
	return []Action{
		{Kind: DeleteTask},
		{Kind: WriteRun, Command: quoteExe(exe)},
		{Kind: WriteApproved},
		{Kind: DeleteShortcut},
	}
}

func quoteExe(exe string) string {
	return `"` + strings.ReplaceAll(exe, `"`, "") + `"`
}

var offDetail = i18n.T("开机不会自动打开。", "Does not start at login.")

type State struct {
	Enabled bool      `json:"enabled"`
	Mode    string    `json:"mode"`
	Detail  i18n.Text `json:"detail"`
}

func Describe(exe string, snap Snapshot) State {
	if snap.TaskPresent && snap.TaskEnabled && sameExe(snap.TaskCommand, exe) {
		return State{Enabled: true, Mode: "logon-task", Detail: i18n.T("已写入当前用户的登录任务，登录约 15 秒后启动。", "Registered as a logon task for the current user; starts about 15 seconds after login.")}
	}
	if snap.RunCommand != "" && snap.Approved != 3 && sameExe(unquote(snap.RunCommand), exe) {
		return State{Enabled: true, Mode: "run-key", Detail: i18n.T("登录任务未能写入，已改用当前用户的启动项。", "Could not register a logon task; using the current user's Run key instead.")}
	}
	return State{Enabled: false, Mode: "off", Detail: offDetail}
}

func sameExe(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
