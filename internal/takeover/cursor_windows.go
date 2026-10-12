//go:build windows

package takeover

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// cursorKillArgs 只结束 Cursor.exe 自己。
// 不能加 /T：Remote WSL 的 wsl.exe 是 Cursor 的子进程，/T 会把它一起强制结束。
// 多个窗口同时再去拉起 wsl.exe 时，mirrored 模式下的 WSLService 会返回 E_UNEXPECTED（灾难性故障）。
func cursorKillArgs() []string {
	return []string{"/F", "/IM", "Cursor.exe"}
}

func TerminateCursor() error {
	out, err := exec.Command("taskkill", cursorKillArgs()...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(text), "not found") || strings.Contains(text, "没有找到") || strings.Contains(text, "找不到") {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 128 {
		return nil
	}
	if text == "" {
		return err
	}
	return fmt.Errorf("%s", text)
}
