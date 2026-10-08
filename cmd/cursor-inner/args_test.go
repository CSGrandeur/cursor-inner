package main

import "testing"

func TestClassicConsoleFlag(t *testing.T) {
	if !classicConsole([]string{"--classic-console"}) {
		t.Fatal("flag not recognized")
	}
	if classicConsole([]string{"--debug"}) {
		t.Fatal("unrelated flag treated as classic console")
	}
}
