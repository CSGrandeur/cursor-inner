package takeover

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// 主进程被强杀时 Shutdown 不会执行，守护进程要补关接管的 Grok / Codex，并记下供下次一键恢复。
func TestWatchClosesManagedAppsAfterCrash(t *testing.T) {
	dir := t.TempDir()
	var closed []string
	oldG, oldC := watchGrokClose, watchCodexClose
	watchGrokClose = func(p string) (bool, error) { closed = append(closed, "grok:"+p); return true, nil }
	watchCodexClose = func(p string) (bool, error) { closed = append(closed, "codex:"+p); return false, nil }
	defer func() { watchGrokClose, watchCodexClose = oldG, oldC }()

	recoverAfterExit(dir, 999999)
	if len(closed) != 0 {
		t.Fatalf("没有接管标记时不该关，实际 %v", closed)
	}
	_ = os.WriteFile(filepath.Join(dir, "grok.managed"), []byte("http://127.0.0.1:41001\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "codex.managed"), []byte("1\n"), 0o644)
	recoverAfterExit(dir, 999999)
	if !slices.Equal(closed, []string{"grok:http://127.0.0.1:41001", "codex:"}) {
		t.Fatalf("强杀后（只关带本实例代理的；旧标记退回旧判断）应补关接管的 Grok/Codex，实际 %v", closed)
	}
	if got := PendingRestore(dir); !slices.Equal(got, []string{"grok"}) {
		t.Fatalf("只记真正关掉的，got %v", got)
	}
}
