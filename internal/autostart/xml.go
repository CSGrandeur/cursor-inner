package autostart

import (
	"encoding/xml"
	"strings"
)

func TaskXML(exe, user string) string {
	workdir := dirOf(exe)
	userPrincipal := ""
	userTrigger := ""
	if strings.TrimSpace(user) != "" {
		escaped := xmlEscape(user)
		userPrincipal = "<UserId>" + escaped + "</UserId>"
		userTrigger = "<UserId>" + escaped + "</UserId>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>cursor-inner</Description>
    <URI>\cursor-inner</URI>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      ` + userPrincipal + `
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>6</Priority>
  </Settings>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      ` + userTrigger + `
      <Delay>PT15S</Delay>
    </LogonTrigger>
  </Triggers>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(exe) + `</Command>
      <WorkingDirectory>` + xmlEscape(workdir) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>`
}

func dirOf(exe string) string {
	exe = strings.TrimRight(exe, `\/`)
	if i := strings.LastIndexAny(exe, `\/`); i > 0 {
		return exe[:i]
	}
	return exe
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func ParseTaskXML(text string) (command string, enabled bool) {
	settings := between(text, "<Settings>", "</Settings>")
	enabled = strings.Contains(settings, "<Enabled>true</Enabled>")
	return strings.TrimSpace(between(text, "<Command>", "</Command>")), enabled
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return ""
	}
	return s[:j]
}
