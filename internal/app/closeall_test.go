package app

import (
	"errors"
	"slices"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/takeover"
)

type closeFakes struct{ closed, opened []string }

func fakeApps(t *testing.T, grokUp, codexUp bool) *closeFakes {
	f := &closeFakes{}
	oG, oC, oGR, oCR, oCu, oGO, oCO := grokClose, codexClose, grokManagedRunning, codexManagedRunning, cursorRunning, grokOpen, codexOpen
	t.Cleanup(func() {
		grokClose, codexClose, grokManagedRunning, codexManagedRunning, cursorRunning, grokOpen, codexOpen = oG, oC, oGR, oCR, oCu, oGO, oCO
	})
	grokClose = func(string) (bool, error) { f.closed = append(f.closed, "grok"); return grokUp, nil }
	codexClose = func(string) (bool, error) { f.closed = append(f.closed, "codex"); return codexUp, nil }
	grokManagedRunning = func(string) bool { return grokUp }
	codexManagedRunning = func(string) bool { return codexUp }
	cursorRunning = func() bool { return true }
	grokOpen = func(*App) error { f.opened = append(f.opened, "grok"); return nil }
	codexOpen = func(*App) error { f.opened = append(f.opened, "codex"); return errors.New("no proxy") }
	return f
}

func newCloseApp(t *testing.T) *App {
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{store: store}
}

// 复现 10-14：退出 cursor-inner 后，被接管（带本程序代理参数）的 Grok Bot / Codex 仍在运行。
func TestShutdownClosesManagedGrokAndCodexAndRemembers(t *testing.T) {
	f := fakeApps(t, true, false)
	a := newCloseApp(t)
	a.keepMark("grok.managed", "")
	a.keepMark("codex.managed", "")
	a.closeManaged()
	if !slices.Equal(f.closed, []string{"grok", "codex"}) {
		t.Fatalf("退出时应关掉接管的 Grok 与 Codex，实际 %v", f.closed)
	}
	if got := takeover.PendingRestore(a.store.Dir()); !slices.Equal(got, []string{"grok"}) {
		t.Fatalf("只记真正关掉的：%v", got)
	}
}

func TestShutdownLeavesUnmanagedOrIsolatedAlone(t *testing.T) {
	f := fakeApps(t, true, true)
	a := newCloseApp(t)
	a.closeManaged()
	a.keepMark("grok.managed", "")
	a.isolated.Store(true)
	a.closeManaged()
	if len(f.closed) != 0 {
		t.Fatalf("没接管过或调试实例都不能关，实际 %v", f.closed)
	}
}

func TestQuitPlanListsAndHonoursDontAsk(t *testing.T) {
	fakeApps(t, true, false)
	a := newCloseApp(t)
	if p := a.QuitPlan(); p.Confirm || len(p.Closes) != 0 {
		t.Fatalf("什么都不关时不用确认：%+v", p)
	}
	a.keepMark("grok.managed", "")
	if err := takeover.Mark(a.store.Dir()); err != nil {
		t.Fatal(err)
	}
	p := a.QuitPlan()
	if !p.Confirm || !slices.Equal(p.Closes, []string{"cursor", "grok"}) {
		t.Fatalf("plan %+v", p)
	}
	quit := false
	a.OnQuit(func() { quit = true })
	if err := a.QuitConfirmed(true); err != nil || !quit {
		t.Fatalf("err=%v quit=%v", err, quit)
	}
	if p := a.QuitPlan(); p.Confirm {
		t.Fatal("选了不再询问后不该再确认")
	}
}

func TestRestoreReopensWithProxyAndKeepsFailures(t *testing.T) {
	f := fakeApps(t, false, false)
	a := newCloseApp(t)
	takeover.NoteRestore(a.store.Dir(), "grok", "codex")
	if p := a.QuitPlan(); !slices.Equal(p.Restore, []string{"grok", "codex"}) {
		t.Fatalf("restore %v", p.Restore)
	}
	if err := a.RestoreApps(false); err == nil {
		t.Fatal("Codex 打不开应报错")
	}
	if !slices.Equal(f.opened, []string{"grok", "codex"}) {
		t.Fatalf("opened %v", f.opened)
	}
	if got := takeover.PendingRestore(a.store.Dir()); !slices.Equal(got, []string{"codex"}) {
		t.Fatalf("失败的留着，got %v", got)
	}
	if err := a.store.Update(func(c *config.File) error { c.TakeoverCodex = false; return nil }); err != nil {
		t.Fatal(err)
	}
	f.opened = nil
	if err := a.RestoreApps(false); err == nil || len(f.opened) != 0 {
		t.Fatalf("接管关了就不能启动（不直连）：err=%v opened=%v", err, f.opened)
	}
	if err := a.RestoreApps(true); err != nil || len(takeover.PendingRestore(a.store.Dir())) != 0 {
		t.Fatal("忽略应清空")
	}
}
