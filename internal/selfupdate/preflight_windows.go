//go:build windows

package selfupdate

import (
	"context"
	"os/exec"

	"cursor-inner/internal/winproc"
)

// 新版本是控制台程序时，从图形界面的旧进程直接起它会闪一下黑框。
func versionCommand(ctx context.Context, exe string) *exec.Cmd {
	return winproc.QuietContext(ctx, exe, "--version")
}
