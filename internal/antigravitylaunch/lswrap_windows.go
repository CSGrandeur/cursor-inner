//go:build windows

package antigravitylaunch

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	lsName     = "language_server.exe"
	lsRealName = "language_server.exe.cursor-inner-real"
	lsMarkName = "language_server.exe.cursor-inner-wrap"
)

// RestoreLanguageServer 把早期版本留下的 language_server.exe 包装自愈还原成官方二进制。
// 若未曾包装（不存在 .cursor-inner-real），静默返回 nil。绝不写新包装，也不触碰任何 WSL 文件。
func RestoreLanguageServer(antiExe string) error {
	binDir, err := languageServerDir(antiExe)
	if err != nil {
		return nil
	}
	return restoreLanguageServer(binDir)
}

func languageServerDir(antiExe string) (string, error) {
	antiExe = strings.TrimSpace(antiExe)
	if antiExe == "" {
		return "", fmt.Errorf("empty antigravity exe")
	}
	dir := filepath.Join(filepath.Dir(antiExe), "resources", "bin")
	if _, err := os.Stat(filepath.Join(dir, lsName)); err == nil {
		return dir, nil
	}
	if _, err := os.Stat(filepath.Join(dir, lsRealName)); err == nil {
		return dir, nil
	}
	return "", fmt.Errorf("language_server not found under %s", dir)
}

func restoreLanguageServer(binDir string) error {
	exe := filepath.Join(binDir, lsName)
	real := filepath.Join(binDir, lsRealName)
	mark := filepath.Join(binDir, lsMarkName)
	old := exe + ".cursor-inner-old"
	tmpNew := exe + ".cursor-inner-new"
	if _, err := os.Stat(real); err != nil {
		if os.IsNotExist(err) {
			_ = os.Remove(mark)
			_ = os.Remove(old)
			_ = os.Remove(tmpNew)
			return nil
		}
		return err
	}
	if err := os.Remove(exe); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(old)
		if rerr := os.Rename(exe, old); rerr != nil {
			return err
		}
	}
	if err := os.Rename(real, exe); err != nil {
		return err
	}
	_ = os.Remove(mark)
	_ = os.Remove(old)
	_ = os.Remove(tmpNew)
	slog.Info("已还原 Antigravity language_server")
	return nil
}
