package program

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

func TestConcealCaptionClose(t *testing.T) {
	if !concealCaptionClose("ConsoleWindowClass", hitClose) {
		t.Fatal("close button")
	}
	if concealCaptionClose("ConsoleWindowClass", 1) || concealCaptionClose("PseudoConsoleWindow", hitClose) {
		t.Fatal("other clicks stay")
	}
	if !concealAltF4("ConsoleWindowClass", vkF4, llkhfAltDown) {
		t.Fatal("alt f4")
	}
	if concealAltF4("ConsoleWindowClass", vkF4, llkhfAltDown|llkhfUp) || concealAltF4("PseudoConsoleWindow", vkF4, llkhfAltDown) {
		t.Fatal("keyup and other windows stay")
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
