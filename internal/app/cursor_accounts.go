package app

import (
	"context"
	"log/slog"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursoraccount"
	"cursor-inner/internal/cursorlaunch"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/web"
)

var (
	cursorStopIDE = takeover.TerminateCursor
	cursorOpenURL = openBrowserURL
)

func (a *App) cursorAccounts() *cursoraccount.Manager {
	a.cursorOnce.Do(func() {
		store, err := cursoraccount.LoadStore(a.store.Dir())
		if err != nil {
			slog.Warn("Cursor 账号文件加载失败", "error", err.Error(), logx.FileOnly)
			a.cursorErr = i18n.Of(err)
			return
		}
		m := cursoraccount.NewManager(store, func() config.Proxy { return a.store.Get().Proxy })
		m.SetHooks(
			func() error {
				if a.isolated.Load() {
					return i18n.E("调试实例不结束正在运行的 Cursor", "The debug instance does not stop a running Cursor")
				}
				_ = cursorlaunch.RunningExe()
				return cursorStopIDE()
			},
			func() error { return a.OpenCursor() },
			func() bool { return cursorRunning() },
			cursorOpenURL,
		)
		m.SetExePath(cursorlaunch.RunningExe)
		m.EnableHostDetection()
		a.cursorMgr = m
	})
	return a.cursorMgr
}

func (a *App) cursorReady() (*cursoraccount.Manager, error) {
	if m := a.cursorAccounts(); m != nil {
		return m, nil
	}
	return nil, &i18n.Error{Text: a.cursorErr}
}

func cursorWebView(v cursoraccount.View) web.AntiAccountView {
	var quotas []web.AntiQuotaView
	if len(v.Quotas) > 0 {
		quotas = make([]web.AntiQuotaView, len(v.Quotas))
		for i, q := range v.Quotas {
			quotas[i] = web.AntiQuotaView{
				Label:   q.Label,
				Percent: q.Percent,
				ResetAt: q.ResetAt,
				Window:  q.Window,
			}
		}
	}
	return web.AntiAccountView{
		ID: v.ID, Email: v.Email, Name: v.Name,
		Tier: v.Tier, Quotas: quotas, Status: v.Status,
		CreatedAt: v.CreatedAt, LastUsed: v.LastUsed,
		Current: v.Current, Expiry: v.Expiry,
	}
}

func cursorImportResults(rs []cursoraccount.ImportResult) []web.AntiImportResult {
	out := make([]web.AntiImportResult, 0, len(rs))
	for _, r := range rs {
		out = append(out, web.AntiImportResult{Email: r.Email, OK: r.OK, Error: r.Error})
	}
	return out
}

func (a *App) CursorAccountList() []web.AntiAccountView {
	m, err := a.cursorReady()
	if err != nil {
		return []web.AntiAccountView{}
	}
	var out []web.AntiAccountView
	for _, v := range m.List() {
		out = append(out, cursorWebView(v))
	}
	if out == nil {
		out = []web.AntiAccountView{}
	}
	return out
}

func (a *App) CursorAccountLoginStart() (string, error) {
	m, err := a.cursorReady()
	if err != nil {
		return "", err
	}
	url, err := m.LoginStart()
	if err != nil {
		return "", err
	}
	st := &antiLoginState{running: true}
	a.cursorLogin.Store(st)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		view, err := m.LoginWait(ctx)
		st.mu.Lock()
		st.running, st.done = false, true
		st.view = cursorWebView(view)
		if err != nil {
			st.err = i18n.Of(err)
		}
		st.mu.Unlock()
	}()
	return url, nil
}

func (a *App) CursorAccountLoginState() web.AntiLoginState {
	v := a.cursorLogin.Load()
	if v == nil {
		return web.AntiLoginState{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	out := web.AntiLoginState{Running: v.running, Done: v.done, Error: v.err}
	if v.done && v.err.IsZero() {
		out.Account = &v.view
	}
	return out
}

func (a *App) CursorAccountLoginCancel() {
	if m := a.cursorAccounts(); m != nil {
		m.LoginCancel()
	}
	if v := a.cursorLogin.Load(); v != nil {
		v.mu.Lock()
		v.running, v.done = false, true
		v.err = i18n.T("授权已取消", "Authorization cancelled")
		v.mu.Unlock()
	}
}

func (a *App) CursorAccountImport(ctx context.Context, rawToken string) (web.AntiAccountView, error) {
	m, err := a.cursorReady()
	if err != nil {
		return web.AntiAccountView{}, err
	}
	v, err := m.Import(ctx, rawToken)
	return cursorWebView(v), err
}

func (a *App) CursorAccountImportCurrent(ctx context.Context) (web.AntiAccountView, error) {
	m, err := a.cursorReady()
	if err != nil {
		return web.AntiAccountView{}, err
	}
	v, err := m.ImportCurrent(ctx)
	return cursorWebView(v), err
}

func (a *App) CursorAccountImportJSON(ctx context.Context, raw []byte) ([]web.AntiImportResult, error) {
	m, err := a.cursorReady()
	if err != nil {
		return nil, err
	}
	rs, err := m.ImportJSON(ctx, raw)
	return cursorImportResults(rs), err
}

func (a *App) CursorAccountExportJSON() ([]byte, error) {
	m, err := a.cursorReady()
	if err != nil {
		return nil, err
	}
	return m.ExportJSON()
}

func (a *App) CursorAccountRemove(id string) error {
	m, err := a.cursorReady()
	if err != nil {
		return err
	}
	return m.Remove(id)
}

func (a *App) CursorAccountRefresh(ctx context.Context, id string) (web.AntiAccountView, error) {
	m, err := a.cursorReady()
	if err != nil {
		return web.AntiAccountView{}, err
	}
	v, err := m.Refresh(ctx, id)
	return cursorWebView(v), err
}

func (a *App) CursorAccountRefreshAll(ctx context.Context) (int, int, error) {
	m, err := a.cursorReady()
	if err != nil {
		return 0, 0, err
	}
	return m.RefreshAll(ctx)
}

func (a *App) CursorAccountSwitch(ctx context.Context, id string) error {
	m, err := a.cursorReady()
	if err != nil {
		return err
	}
	if err := m.Switch(ctx, id); err != nil {
		return err
	}
	a.NoteAppUsed("cursor")
	return nil
}
