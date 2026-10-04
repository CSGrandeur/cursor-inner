package autostart

import (
	"strings"
	"testing"
)

func TestTaskXMLCarriesWin11Flags(t *testing.T) {
	xml := TaskXML(`C:\Program Files\cursor-inner.exe`, `DESKTOP-01\alice`)
	for _, needle := range []string{
		"<Delay>PT15S</Delay>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<StopOnIdleEnd>false</StopOnIdleEnd>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<LogonType>InteractiveToken</LogonType>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>",
		`C:\Program Files\cursor-inner.exe`,
		`C:\Program Files`,
		`DESKTOP-01\alice`,
	} {
		if !strings.Contains(xml, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
	if strings.Contains(xml, "RestartOnFailure") {
		t.Fatal("restart on failure would relaunch after the user closes the window")
	}
	cmd, enabled := ParseTaskXML(xml)
	if !enabled || cmd != `C:\Program Files\cursor-inner.exe` {
		t.Fatal(cmd, enabled)
	}
}
