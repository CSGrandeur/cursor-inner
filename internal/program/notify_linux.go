//go:build linux

package program

import (
	"fmt"
	"os"
	"os/exec"
)

func openBrowser(url string) error {
	for _, opener := range [][]string{{"xdg-open"}, {"wslview"}, {"cmd.exe", "/c", "start"}} {
		if path, err := exec.LookPath(opener[0]); err == nil {
			return exec.Command(path, append(opener[1:], url)...).Start()
		}
	}
	return nil
}

func notifyAlreadyRunning(url string) {
	text := alreadyRunningText(url)
	fmt.Fprintln(os.Stderr, text)
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		dialogs := [][]string{
			{"zenity", "--info", "--no-wrap", "--title=cursor-inner", "--text=" + text},
			{"kdialog", "--title", "cursor-inner", "--msgbox", text},
			{"notify-send", "cursor-inner", text},
		}
		for _, dialog := range dialogs {
			if path, err := exec.LookPath(dialog[0]); err == nil {
				_ = exec.Command(path, dialog[1:]...).Run()
				break
			}
		}
	}
	if url != "" {
		_ = openBrowser(url)
	}
}
