package app

import (
	"os"
	"path/filepath"

	"cursor-inner/internal/config"
)

// OpenLogs 打开日志所在的文件夹。有 <data>/logs 目录时打开它，否则打开 <data>（inner.log 所在）。
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
	cmd := folderCommand(dir)
	// explorer 打开成功也可能返回非零码，这里只在意能不能启动。
	_ = cmd.Start()
	return nil
}
