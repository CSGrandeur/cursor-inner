package main

import "testing"

func TestConsoleLaunchAction(t *testing.T) {
	if consoleLaunchAction(false, "") != "relaunch" || consoleLaunchAction(false, "PseudoConsoleWindow") != "relaunch" {
		t.Fatal("double-click and Windows Terminal must leave the terminal job")
	}
	if consoleLaunchAction(false, "ConsoleWindowClass") != "stay" {
		t.Fatal("classic console stays")
	}
	if consoleLaunchAction(true, "ConsoleWindowClass") != "stay" || consoleLaunchAction(true, "") != "alloc" {
		t.Fatal("relaunched child must not relaunch again")
	}
}

func TestCloseButtonHidesWhenResident(t *testing.T) {
	if consoleCloseAction(ctrlCloseEvent, true) != closeHide {
		t.Fatal("close")
	}
	if consoleCloseAction(ctrlCloseEvent, false) != closeQuit {
		t.Fatal("close without tray")
	}
	for _, ctrl := range []uint32{0, 1, 5, 6} {
		if consoleCloseAction(ctrl, true) != closeQuit {
			t.Fatalf("ctrl %d", ctrl)
		}
	}
}
