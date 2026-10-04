package autostart

import "testing"

func TestPlanEnableDoesNotAlsoWriteRunKey(t *testing.T) {
	actions := Plan(true, `C:\app\cursor-inner.exe`, Snapshot{RunCommand: `"C:\old.exe"`, Approved: 3, Shortcut: true})
	if actions[0].Kind != UpsertTask {
		t.Fatal(actions)
	}
	for _, action := range actions {
		if action.Kind == WriteRun || action.Kind == WriteApproved {
			t.Fatal(actions)
		}
	}
	again := Plan(true, `C:\app\cursor-inner.exe`, Snapshot{TaskPresent: true, TaskEnabled: true, TaskCommand: `C:\app\cursor-inner.exe`})
	if len(again) != 1 || again[0].Kind != UpsertTask {
		t.Fatal(again)
	}
}

func TestPlanDisableOnEmptyIsEmpty(t *testing.T) {
	snap := Snapshot{Approved: -1}
	if actions := Plan(false, `C:\a.exe`, snap); len(actions) != 0 {
		t.Fatal(actions)
	}
	actions := Plan(false, `C:\a.exe`, Snapshot{TaskPresent: true, RunCommand: `"C:\a.exe"`, Approved: 2, Shortcut: true})
	if len(actions) != 4 {
		t.Fatal(actions)
	}
}

func TestDescribe(t *testing.T) {
	exe := `C:\app\cursor-inner.exe`
	if got := Describe(exe, Snapshot{TaskPresent: true, TaskEnabled: true, TaskCommand: exe, Approved: -1}); !got.Enabled || got.Mode != "logon-task" {
		t.Fatal(got)
	}
	if got := Describe(exe, Snapshot{RunCommand: `"` + exe + `"`, Approved: 2}); !got.Enabled || got.Mode != "run-key" {
		t.Fatal(got)
	}
	if got := Describe(exe, Snapshot{Approved: -1}); got.Enabled {
		t.Fatal(got)
	}
}
