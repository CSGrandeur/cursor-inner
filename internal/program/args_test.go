package program

import "testing"

func TestClassicConsoleFlag(t *testing.T) {
	if !classicConsole([]string{"--classic-console"}) {
		t.Fatal("flag not recognized")
	}
	if classicConsole([]string{"--debug"}) {
		t.Fatal("unrelated flag treated as classic console")
	}
}

func TestPassthroughArgs(t *testing.T) {
	got := passthroughArgs([]string{"--classic-console", "--debug", "--data-dir", "x", "--handover", "p", "--verbose", "--version"})
	if len(got) != 2 || got[0] != "--debug" || got[1] != "--verbose" {
		t.Fatalf("%v", got)
	}
}
