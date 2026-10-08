package main

import "testing"

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
