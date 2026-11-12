package takeover

import (
	"os"
	"path/filepath"
	"strings"

	"cursor-inner/internal/fsutil"
)

func markPath(dir string) string {
	return filepath.Join(dir, "takeover.on")
}

func Mark(dir string) error {
	return MarkURL(dir, "")
}

// MarkURL 写下接管标记。url 为空时与旧版一样写 "1"；否则记下本机代理地址，
// 便于已接管进程冷启动时在同一地址恢复，而不必依赖交接计划。
func MarkURL(dir, url string) error {
	body := "1\n"
	if strings.TrimSpace(url) != "" {
		body = strings.TrimSpace(url) + "\n"
	}
	return fsutil.WriteFile(markPath(dir), []byte(body))
}

func Marked(dir string) bool {
	_, err := os.Stat(markPath(dir))
	return err == nil
}

// MarkedProxy 读接管标记里记的代理地址。旧版内容是 "1"（或读不到）时返回空。
func MarkedProxy(dir string) string {
	raw, err := os.ReadFile(markPath(dir))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(raw))
	if v == "" || v == "1" {
		return ""
	}
	return v
}

func Unmark(dir string) {
	_ = os.Remove(markPath(dir))
}
