package autostart

import (
	"strings"
	"testing"
)

func TestDesktopExecQuoting(t *testing.T) {
	cases := map[string]string{
		"/opt/cursor-inner/cursor-inner": "/opt/cursor-inner/cursor-inner",
		"/home/a b/cursor-inner":         `"/home/a b/cursor-inner"`,
		`/tmp/$x/100%`:                   `"/tmp/\\$x/100%%"`,
	}
	for in, want := range cases {
		if got := DesktopExec(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestDesktopEntryHasDelayAndExec(t *testing.T) {
	entry := DesktopEntry("/opt/cursor-inner")
	for _, want := range []string{"[Desktop Entry]", "Exec=/opt/cursor-inner\n", "X-GNOME-Autostart-Delay=15", "Terminal=false"} {
		if !strings.Contains(entry, want) {
			t.Fatalf("missing %q in\n%s", want, entry)
		}
	}
}

func TestLaunchAgentEscapesPath(t *testing.T) {
	plist := LaunchAgent("/Applications/A&B/cursor-inner")
	if !strings.Contains(plist, "<string>/Applications/A&amp;B/cursor-inner</string>") {
		t.Fatal(plist)
	}
	if !strings.Contains(plist, "<key>RunAtLoad</key>\n\t<true/>") || !strings.Contains(plist, LaunchAgentLabel) {
		t.Fatal(plist)
	}
}
