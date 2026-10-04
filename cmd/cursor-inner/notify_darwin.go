//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func openBrowser(url string) error {
	return exec.Command("open", url).Start()
}

func notifyAlreadyRunning(url string) {
	text := alreadyRunningText(url)
	fmt.Fprintln(os.Stderr, text)
	_ = exec.Command("osascript",
		"-e", "on run argv",
		"-e", `display dialog (item 1 of argv) with title "cursor-inner" buttons {"好"} default button 1 with icon note`,
		"-e", "end run",
		text).Run()
	if url != "" {
		_ = openBrowser(url)
	}
}
