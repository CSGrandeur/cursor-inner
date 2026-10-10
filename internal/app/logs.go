package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"cursor-inner/internal/config"
)

// OpenLogs 打开日志所在的文件夹。个人记录版把日志放在 <data>/logs 下，发版只有 <data>/inner.log。
func (a *App) OpenLogs() error {
	dir := config.DefaultDir()
	if logs := filepath.Join(dir, "logs"); isDir(logs) {
		dir = logs
	}
	return openFolder(dir)
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func openFolder(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	// explorer 打开成功也可能返回非零码，这里只在意能不能启动。
	_ = cmd.Start()
	return nil
}
