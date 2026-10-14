package autostart

import "testing"

// schtasks /Query /XML 导出时会省略取默认值的 <Enabled>true</Enabled>（Task Scheduler 架构里
// Settings/Enabled 默认 true）。以前回读要求显式写着 true，刚建好的任务被当成关闭，开关一开就弹回。
const exportedTask = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><UserId>PC\u</UserId><Delay>PT15S</Delay></LogonTrigger></Triggers>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <StartWhenAvailable>true</StartWhenAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd></IdleSettings>
  </Settings>
  <Actions Context="Author"><Exec><Command>C:\Program Files\Some App\app.exe</Command><WorkingDirectory>C:\Program Files\Some App</WorkingDirectory></Exec></Actions>
</Task>`

func TestParseTaskXMLEnabledOmittedMeansEnabled(t *testing.T) {
	cmd, on := ParseTaskXML(exportedTask)
	if !on || cmd != `C:\Program Files\Some App\app.exe` {
		t.Fatalf("exported task without <Enabled>: cmd=%q enabled=%v", cmd, on)
	}
	st := Describe(`C:\Program Files\Some App\app.exe`, Snapshot{TaskPresent: true, TaskCommand: cmd, TaskEnabled: on, Approved: -1})
	if !st.Enabled || st.Mode != "logon-task" {
		t.Fatalf("state %+v", st)
	}
}

func TestParseTaskXMLExplicitlyDisabled(t *testing.T) {
	text := `<Task><Settings><Enabled>false</Enabled></Settings><Actions><Exec><Command>C:\a.exe</Command></Exec></Actions></Task>`
	if _, on := ParseTaskXML(text); on {
		t.Fatal("explicit <Enabled>false</Enabled> must read as disabled")
	}
	// 触发器里的 Enabled 不代表任务本身。
	text = `<Task><Triggers><LogonTrigger><Enabled>false</Enabled></LogonTrigger></Triggers><Settings></Settings><Actions><Exec><Command>C:\a.exe</Command></Exec></Actions></Task>`
	if _, on := ParseTaskXML(text); !on {
		t.Fatal("trigger-level Enabled must not disable the task")
	}
}
