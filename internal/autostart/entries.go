package autostart

import (
	"bytes"
	"encoding/xml"
	"strings"
)

const LaunchAgentLabel = "io.github.csgrandeur.cursor-inner"

// DesktopEntry 生成 XDG 自动启动项。Exec 先按桌面文件的引号规则转义，再按字符串规则转义反斜杠。
func DesktopEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=cursor-inner\n" +
		"Comment=Bring your own models into Cursor\n" +
		"Exec=" + DesktopExec(exe) + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		"X-GNOME-Autostart-Delay=15\n"
}

func DesktopExec(exe string) string {
	arg := strings.ReplaceAll(exe, "%", "%%")
	if strings.ContainsAny(arg, " \t\n\"'\\><~|&;$*?#()`") {
		arg = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(arg) + `"`
	}
	return strings.ReplaceAll(arg, `\`, `\\`)
}

func LaunchAgent(exe string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LaunchAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlText(exe) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
</dict>
</plist>
`
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
