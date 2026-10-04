//go:build windows

package takeover

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func settingsPath() (string, error) {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return "", errors.New("没有 APPDATA，无法找到 Cursor 配置")
	}
	return filepath.Join(appdata, "Cursor", "User", "settings.json"), nil
}

func TerminateCursor() error {
	out, err := exec.Command("taskkill", "/F", "/T", "/IM", "Cursor.exe").CombinedOutput()
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
