package codexbot

import "testing"

func TestIsDesktopShell(t *testing.T) {
	cases := []struct {
		exe, cmd string
		want     bool
	}{
		{`C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__2p2nqsd0c76g0\app\Codex.exe`, `"C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__2p2nqsd0c76g0\app\Codex.exe"`, true},
		{`C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__2p2nqsd0c76g0\app\ChatGPT.exe`, "", true},
		{`C:\ProgramData\Programs\Codex\Codex.exe`, `Codex.exe --proxy-server=http://127.0.0.1:1080`, true},
		{`C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__2p2nqsd0c76g0\app\Codex.exe`, `Codex.exe --type=renderer`, false},
		{`C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__2p2nqsd0c76g0\app\resources\codex.exe`, `codex.exe app-server --analytics-default-enabled`, false},
		{`C:\Tools\OpenAI\Codex\bin\codex.exe`, `codex.exe exec hi`, false},
		{`C:\tools\codex.exe`, `codex.exe`, false},
		{`C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__2p2nqsd0c76g0\app\Codex.exe`, `Codex.exe app-server`, false},
	}
	for _, tc := range cases {
		if got := isDesktopShell(tc.exe, tc.cmd); got != tc.want {
			t.Errorf("isDesktopShell(%q, %q)=%v want %v", tc.exe, tc.cmd, got, tc.want)
		}
	}
}
