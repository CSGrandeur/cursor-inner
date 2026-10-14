package app

import (
	"log/slog"

	"cursor-inner/internal/codexbot"
	"cursor-inner/internal/config"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/web"
)

// 测试替换用。生产路径是 grokbot / codexbot / takeover 里的同名函数。
var (
	grokClose           = grokbot.Close
	codexClose          = codexbot.Close
	grokManagedRunning  = grokbot.ManagedRunning
	codexManagedRunning = codexbot.ManagedRunning
	cursorRunning       = takeover.CursorRunning
	grokOpen            = func(a *App) error { return a.OpenGrok() }
	codexOpen           = func(a *App) error { return a.OpenCodex() }
)

// closeManaged 在 cursor-inner 退出时关掉它接管的 Grok Bot 与 Codex，并记下以便下次一键恢复。
// 它们的代理指向本程序的回环端口，本程序一退就连不上（不会改走直连，但会一直不可用）。
// 只关带本程序代理参数的进程；调试实例、没接管过的不动。自更新交接不走这里。
func (a *App) closeManaged() {
	if a.isolated.Load() {
		return
	}
	var closed []string
	if a.ownsMark("grok.managed") {
		ok, err := grokClose(a.markProxy("grok.managed"))
		if err != nil {
			slog.Warn("退出时没能关掉 Grok Bot：" + err.Error())
		}
		if ok {
			closed = append(closed, "grok")
		}
	}
	if a.ownsMark("codex.managed") {
		ok, err := codexClose(a.markProxy("codex.managed"))
		if err != nil {
			slog.Warn("退出时没能关掉 Codex：" + err.Error())
		}
		if ok {
			closed = append(closed, "codex")
		}
	}
	takeover.NoteRestore(a.store.Dir(), closed...)
}

// QuitPlan 列出退出时会关掉的程序、是否需要确认，以及上次退出时关掉、可一键恢复的程序。
func (a *App) QuitPlan() web.QuitPlan {
	plan := web.QuitPlan{Closes: []string{}, Restore: []string{}}
	if !a.isolated.Load() {
		if takeover.Marked(a.store.Dir()) && cursorRunning() {
			plan.Closes = append(plan.Closes, "cursor")
		}
		if a.ownsMark("grok.managed") && grokManagedRunning(a.markProxy("grok.managed")) {
			plan.Closes = append(plan.Closes, "grok")
		}
		if a.ownsMark("codex.managed") && codexManagedRunning(a.markProxy("codex.managed")) {
			plan.Closes = append(plan.Closes, "codex")
		}
	}
	plan.Confirm = len(plan.Closes) > 0 && !a.store.Get().QuitNoConfirm
	plan.Restore = append(plan.Restore, a.pendingRestore()...)
	return plan
}

// pendingRestore 去掉已经又在运行（并已带上代理）的程序。
func (a *App) pendingRestore() []string {
	dir := a.store.Dir()
	var out []string
	for _, n := range takeover.PendingRestore(dir) {
		if (n == "grok" && grokManagedRunning(a.markProxy("grok.managed"))) || (n == "codex" && codexManagedRunning(a.markProxy("codex.managed"))) {
			takeover.ClearRestore(dir, n)
			continue
		}
		out = append(out, n)
	}
	return out
}

// QuitConfirmed 是确认框里点了退出：dontAsk 为真时以后不再询问。
func (a *App) QuitConfirmed(dontAsk bool) error {
	if dontAsk {
		if err := a.store.Update(func(f *config.File) error {
			f.QuitNoConfirm = true
			return nil
		}); err != nil {
			return err
		}
	}
	return a.Quit()
}

// RestoreApps 重新打开上次退出时关掉的 Grok / Codex，按当前接管设置带代理；拿不到可靠代理就报错，
// 绝不以直连方式启动。dismiss 为真时只清掉提示。
func (a *App) RestoreApps(dismiss bool) error {
	dir := a.store.Dir()
	if dismiss {
		takeover.ClearRestore(dir)
		return nil
	}
	cfg := a.store.Get()
	var firstErr error
	for _, n := range a.pendingRestore() {
		var err error
		switch {
		case n == "grok" && cfg.TakeoverGrok:
			err = grokOpen(a)
		case n == "codex" && cfg.TakeoverCodex:
			err = codexOpen(a)
		default:
			err = i18n.E("接管已关闭，没有恢复（不会以直连方式启动）", "Takeover is off, so it was not restored (it is never started without the proxy)")
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		takeover.ClearRestore(dir, n)
	}
	return firstErr
}
