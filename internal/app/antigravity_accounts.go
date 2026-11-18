package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"cursor-inner/internal/antiaccount"
	"cursor-inner/internal/antigravitylaunch"
	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/web"
)

// 反重力账号管理。凭据读写在 antiaccount；这里只装配代理设置、进程操作与浏览器，
// 并把「登录」拆成 start（立即返回授权链接）+ state（页面轮询结果）两步，
// 免得一个 HTTP 请求挂住五分钟。

var (
	antiStopIDE = antigravitylaunch.StopAll
	antiRunning = antigravitylaunch.Running
	antiOpenURL = openBrowserURL
)

func (a *App) antiAccounts() *antiaccount.Manager {
	a.antiOnce.Do(func() {
		store, err := antiaccount.LoadStore(a.store.Dir())
		if err != nil {
			// 文件坏了不能静默覆盖：报错并让账号功能不可用，原文件留在原处供排查。
			slog.Warn("反重力账号文件加载失败", "error", err.Error(), logx.FileOnly)
			a.antiErr = i18n.Of(err)
			return
		}
		m := antiaccount.NewManager(store, func() config.Proxy { return a.store.Get().Proxy })
		m.SetHooks(
			func() error {
				if a.isolated.Load() {
					return i18n.E("调试实例不结束正在运行的反重力", "The debug instance does not stop a running Antigravity")
				}
				return antiStopIDE()
			},
			func() error { return a.OpenAntigravity() },
			antiRunning,
			antiOpenURL,
		)
		m.SetExePath(antigravitylaunch.ExePath)
		m.SetResourcesDirs(antigravitylaunch.ResourcesDirs)
		m.EnableHostDetection()
		a.antiMgr = m
	})
	return a.antiMgr
}

// antiReady 取出账号管理器；文件加载失败时返回可行动的错误。
func (a *App) antiReady() (*antiaccount.Manager, error) {
	if m := a.antiAccounts(); m != nil {
		return m, nil
	}
	return nil, &i18n.Error{Text: a.antiErr}
}

// antiLoginState 是进行中的授权流程状态。页面 start 后轮询 state。
type antiLoginState struct {
	mu      sync.Mutex
	running bool
	done    bool
	view    web.AntiAccountView
	err     i18n.Text
}

func antiWebView(v antiaccount.View) web.AntiAccountView {
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

func antiImportResults(rs []antiaccount.ImportResult) []web.AntiImportResult {
	out := make([]web.AntiImportResult, 0, len(rs))
	for _, r := range rs {
		out = append(out, web.AntiImportResult{Email: r.Email, OK: r.OK, Error: r.Error})
	}
	return out
}

func (a *App) AntiAccountList() []web.AntiAccountView {
	m, err := a.antiReady()
	if err != nil {
		return []web.AntiAccountView{}
	}
	var out []web.AntiAccountView
	for _, v := range m.List() {
		out = append(out, antiWebView(v))
	}
	if out == nil {
		out = []web.AntiAccountView{}
	}
	return out
}

// AntiAccountLoginStart 开始授权：返回授权链接（页面展示可复制），后台等回调。
func (a *App) AntiAccountLoginStart() (string, error) {
	m, err := a.antiReady()
	if err != nil {
		return "", err
	}
	url, err := m.LoginStart()
	if err != nil {
		return "", err
	}
	st := &antiLoginState{running: true}
	a.antiLogin.Store(st)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		view, err := m.LoginWait(ctx)
		st.mu.Lock()
		st.running, st.done = false, true
		st.view = antiWebView(view)
		if err != nil {
			st.err = i18n.Of(err)
		}
		st.mu.Unlock()
	}()
	return url, nil
}

// AntiAccountLoginState 给页面轮询：running / done / error。
func (a *App) AntiAccountLoginState() web.AntiLoginState {
	v := a.antiLogin.Load()
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

func (a *App) AntiAccountLoginCancel() {
	if m := a.antiAccounts(); m != nil {
		m.LoginCancel()
	}
	if v := a.antiLogin.Load(); v != nil {
		v.mu.Lock()
		v.running, v.done = false, true
		v.err = i18n.T("授权已取消", "Authorization cancelled")
		v.mu.Unlock()
	}
}

func (a *App) AntiAccountImport(ctx context.Context, refreshToken string) (web.AntiAccountView, error) {
	m, err := a.antiReady()
	if err != nil {
		return web.AntiAccountView{}, err
	}
	v, err := m.Import(ctx, refreshToken)
	return antiWebView(v), err
}

func (a *App) AntiAccountImportCurrent(ctx context.Context) (web.AntiAccountView, error) {
	m, err := a.antiReady()
	if err != nil {
		return web.AntiAccountView{}, err
	}
	v, err := m.ImportCurrent(ctx)
	return antiWebView(v), err
}

func (a *App) AntiAccountImportJSON(ctx context.Context, raw []byte) ([]web.AntiImportResult, error) {
	m, err := a.antiReady()
	if err != nil {
		return nil, err
	}
	rs, err := m.ImportJSON(ctx, raw)
	return antiImportResults(rs), err
}

func (a *App) AntiAccountExportJSON() ([]byte, error) {
	m, err := a.antiReady()
	if err != nil {
		return nil, err
	}
	return m.ExportJSON()
}

func (a *App) AntiAccountRemove(id string) error {
	m, err := a.antiReady()
	if err != nil {
		return err
	}
	return m.Remove(id)
}

func (a *App) AntiAccountRefresh(ctx context.Context, id string) (web.AntiAccountView, error) {
	m, err := a.antiReady()
	if err != nil {
		return web.AntiAccountView{}, err
	}
	v, err := m.Refresh(ctx, id)
	return antiWebView(v), err
}

func (a *App) AntiAccountRefreshAll(ctx context.Context) (int, int, error) {
	m, err := a.antiReady()
	if err != nil {
		return 0, 0, err
	}
	return m.RefreshAll(ctx)
}

// AntiAccountSwitch 切号。调试实例不结束正在运行的反重力（hooks 里已挡）；
// 切换期间暂停 1 秒巡检，避免 SyncAntigravity 在半途重启反重力。
func (a *App) AntiAccountSwitch(ctx context.Context, id string) error {
	m, err := a.antiReady()
	if err != nil {
		return err
	}
	a.antiSwitching.Store(true)
	defer a.antiSwitching.Store(false)
	if err := m.Switch(ctx, id); err != nil {
		return err
	}
	a.NoteAppUsed("antigravity")
	return nil
}
