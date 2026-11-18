package app

import (
	"path/filepath"
	"testing"

	"cursor-inner/internal/config"
)

func TestRecentAppsOrderPersistsAndTracksForeground(t *testing.T) {
	oldSeed, oldFg := seedRecentAppsFn, foregroundExeFn
	seedRecentAppsFn = func() (int64, int64) { return 10, 20 }
	var currentFG string
	foregroundExeFn = func() string { return currentFG }
	t.Cleanup(func() {
		seedRecentAppsFn = oldSeed
		foregroundExeFn = oldFg
	})

	dir := t.TempDir()
	store, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store}

	// 冷启动种子是 antigravity(20) > cursor(10)
	order := a.AccountOrder()
	if len(order) != 2 || order[0] != "antigravity" || order[1] != "cursor" {
		t.Fatalf("seed order = %v, want [antigravity cursor]", order)
	}

	// 前台焦点切到 Cursor.exe 后立即反超排第一并落盘
	currentFG = filepath.Join(dir, "Cursor.exe")
	a.TickRecentApps()
	order = a.AccountOrder()
	if order[0] != "cursor" || order[1] != "antigravity" {
		t.Fatalf("after Cursor fg = %v, want [cursor antigravity]", order)
	}

	// 重建 App 实例读 recent-apps.json 仍保持 Cursor 在前
	b := &App{store: store}
	order2 := b.AccountOrder()
	if order2[0] != "cursor" || order2[1] != "antigravity" {
		t.Fatalf("reloaded order = %v, want [cursor antigravity]", order2)
	}

	// 再切回 Antigravity.exe
	currentFG = filepath.Join(dir, "Antigravity.exe")
	b.TickRecentApps()
	if got := b.AccountOrder(); got[0] != "antigravity" {
		t.Fatalf("after Antigravity fg = %v, want antigravity first", got)
	}
}
