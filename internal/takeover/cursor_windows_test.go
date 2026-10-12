//go:build windows

package takeover

import "testing"

func TestTerminateCursorDoesNotKillTheProcessTree(t *testing.T) {
	args := cursorKillArgs()
	for _, a := range args {
		if a == "/T" || a == "/t" {
			t.Fatal("taskkill /T also kills wsl.exe, which takes down WSLService")
		}
	}
	if len(args) < 2 || args[len(args)-1] != "Cursor.exe" {
		t.Fatalf("args %q", args)
	}
}
