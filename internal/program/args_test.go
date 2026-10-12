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

// --background 由旧进程按控制台此刻的状态重新决定，不原样传下去；--console-holder 带两个参数。
func TestBackgroundAndHolderArgs(t *testing.T) {
	opt := parseArgs([]string{"--background", "--console-holder", "42", `C:\t\h.pid`, "--debug"})
	if !opt.background || opt.holder != 42 || opt.holderFile != `C:\t\h.pid` || !opt.debug {
		t.Fatalf("%+v", opt)
	}
	if got := passthroughArgs([]string{"--background", "--verbose"}); len(got) != 1 || got[0] != "--verbose" {
		t.Fatalf("background must not be passed through: %v", got)
	}
	if opt := parseArgs([]string{"--console-holder", "42"}); opt.holder != 0 {
		t.Fatalf("incomplete holder args must be ignored: %+v", opt)
	}
}
