//go:build !windows

package selfupdate

import (
	"context"
	"os/exec"
)

func versionCommand(ctx context.Context, exe string) *exec.Cmd {
	return exec.CommandContext(ctx, exe, "--version")
}
