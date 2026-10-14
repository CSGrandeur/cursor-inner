package takeover

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor-inner/internal/codexbot"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/selfupdate"
)

func recoverAfterExit(dir string, pid int) {
	// 自更新交接：旧进程退出是把接管交给了新进程，新进程有自己的守护进程，这里什么都不做。
	if selfupdate.HandedOver(dir, pid) {
		return
	}
	// 主进程被强杀或崩溃时没走 Shutdown：在这里补关接管的 Grok Bot / Codex。
	closeManagedApps(dir)
	if !Marked(dir) {
		return
	}
	err := Recover(dir)
	line := time.Now().Format(time.RFC3339) + " 主进程已退出，已撤掉接管并结束 Cursor\n"
	if err != nil {
		line = time.Now().Format(time.RFC3339) + " 主进程已退出，撤掉接管失败：" + err.Error() + "\n"
	}
	_ = os.MkdirAll(dir, 0o755)
	f, openErr := os.OpenFile(filepath.Join(dir, "watch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// 测试替换用。
var (
	watchGrokClose  = grokbot.Close
	watchCodexClose = codexbot.Close
)

// closeManagedApps 关掉带本程序代理参数的 Grok Bot / Codex（只在对应接管标记存在时）。
func closeManagedApps(dir string) {
	var closed []string
	if _, err := os.Stat(filepath.Join(dir, "grok.managed")); err == nil {
		if ok, _ := watchGrokClose(MarkProxy(dir, "grok.managed")); ok {
			closed = append(closed, "grok")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "codex.managed")); err == nil {
		if ok, _ := watchCodexClose(MarkProxy(dir, "codex.managed")); ok {
			closed = append(closed, "codex")
		}
	}
	NoteRestore(dir, closed...)
}

// MarkProxy 读接管标记里记的代理地址。旧版标记内容是 "1"（或读不到）时返回空。
func MarkProxy(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(raw))
	if v == "1" {
		return ""
	}
	return v
}
