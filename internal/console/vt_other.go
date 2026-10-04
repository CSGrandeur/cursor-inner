//go:build !windows

package console

import "os"

func enableVT(*os.File) bool { return os.Getenv("TERM") != "dumb" }

func disableEcho(*os.File) {}
