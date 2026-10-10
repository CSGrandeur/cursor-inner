package app

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/autostart"
	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorlaunch"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/selfupdate"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/tunnel"
	"cursor-inner/internal/web"
)

type starter interface {
	Apply(enabled bool, exe string) (autostart.State, error)
	Current(exe string) autostart.State
}

type App struct {
	store    *config.Store
	life     *takeover.Service
	boot     starter
	exe      string
	mu       sync.Mutex
	listen   string
	bootErr  i18n.Text
	quit     func()
	shutdown sync.Once
	bridge   grokbot.Bridge
	tun      *tunnel.Manager
	upd      *selfupdate.Updater
	frozen   atomic.Bool
}

func (a *App) OnQuit(fn func()) {
	a.mu.Lock()
	a.quit = fn
	a.mu.Unlock()
}

func (a *App) Quit() error {
	a.mu.Lock()
	fn := a.quit
	a.mu.Unlock()
	if fn == nil {
		return i18n.E("程序还在启动，稍后再试", "Still starting up, try again in a moment")
	}
	fn()
	return nil
}

func New(store *config.Store, life *takeover.Service, boot starter, exe string) *App {
	return &App{store: store, life: life, boot: boot, exe: exe,
		tun: tunnel.NewManager(filepath.Join(config.DefaultDir(), "tun"))}
}

func (a *App) SetListen(url string) {
	a.mu.Lock()
	a.listen = url
	a.mu.Unlock()
}

func (a *App) SyncAutostart() (autostart.State, error) {
	state, err := a.boot.Apply(a.store.Get().Autostart, a.exe)
	a.mu.Lock()
	if err != nil {
		a.bootErr = i18n.Of(err)
	} else {
		a.bootErr = i18n.Text{}
	}
	a.mu.Unlock()
	return state, err
}

func (a *App) EnableTakeover() error {
	if err := a.store.Update(func(f *config.File) error {
		f.Takeover = true
		return nil
	}); err != nil {
		return err
	}
	return a.life.Enable()
}

func (a *App) DisableTakeover() error {
	if err := a.life.Stop(); err != nil {
		return err
	}
	return a.store.Update(func(f *config.File) error {
		f.Takeover = false
		return nil
	})
}

func (a *App) OpenCursor() error {
	return cursorlaunch.Start()
}

// Shutdown 只还原 Cursor。Grok Bot 保持原样：它直连用户配置的代理，不经过本程序，
// 退出时若改回无代理重开，流量会直连出去。要撤掉 Grok 的代理，请在配置页关掉「接管 Grok」。
func (a *App) Shutdown() {
	a.shutdown.Do(func() {
		_ = a.life.Restore()
	})
}

func (a *App) State() (web.View, error) {
	cfg := a.store.Get()
	snap := a.life.Snapshot()
	spec, on, perr := dialer.EffectiveAddress(cfg.Proxy)
	_ = spec
	view := web.View{
		ListenURL:      a.listenURL(),
		Takeover:       cfg.Takeover,
		TakeoverGrok:   cfg.TakeoverGrok,
		TakeoverActive: snap.Active,
		MitmURL:        snap.URL,
		CA:             snap.CA,
		CADetail:       snap.Detail,
		LastError:      snap.Problem,
		CatalogWarning: snap.Warning,
		Autostart:      a.boot.Current(a.exe),
	}
	view.Proxy.Enabled = cfg.Proxy.Enabled
	view.Proxy.Address = cfg.Proxy.Address
	view.Proxy.Effective = on && perr == nil
	view.Image.BaseURL = cfg.Image.BaseURL
	view.Image.Model = cfg.Image.Model
	view.Image.KeyHint = config.Model{APIKey: cfg.Image.APIKey}.KeyHint()
	if perr != nil && view.LastError.IsZero() {
		view.LastError = i18n.Of(perr)
	}
	a.mu.Lock()
	if view.LastError.IsZero() {
		view.LastError = a.bootErr
	}
	a.mu.Unlock()
	tstate, tdetail := a.tun.Status()
	view.Tunnel.Status = int(tstate)
	view.Tunnel.Detail = tdetail
	for _, model := range cfg.Models {
		view.Models = append(view.Models, web.ModelView{
			ID:              model.ID,
			DisplayName:     model.DisplayName,
			Type:            model.Type,
			BaseURL:         model.BaseURL,
			Model:           model.Model,
			UseProxy:        model.UseProxy,
			Reasoning:       model.Reasoning,
			Fast:            model.FastSupport,
			ContextWindow:   model.ContextWindow,
			MaxOutputTokens: model.MaxOutputTokens,
			PromptCacheKey:  model.PromptCacheKey,
			Fallback:        append([]string(nil), model.Fallback...),
			KeyHint:         model.KeyHint(),
			LastTest:        model.LastTest,
		})
	}
	if view.Models == nil {
		view.Models = []web.ModelView{}
	}
	if view.CA == "" {
		view.CA = "missing"
	}
	return view, nil
}

func (a *App) SetTakeover(target string, enabled bool) error {
	switch target {
	case "", "cursor":
		if enabled {
			return a.EnableTakeover()
		}
		return a.DisableTakeover()
	case "grok":
		if err := a.store.Update(func(f *config.File) error {
			f.TakeoverGrok = enabled
			return nil
		}); err != nil {
			return err
		}
		a.SyncGrok()
		return nil
	default:
		return i18n.E("未知的接管目标", "Unknown takeover target")
	}
}

func (a *App) SetProxy(enabled bool, address string) error {
	next := config.Proxy{Enabled: enabled, Address: address}
	if _, _, err := dialer.EffectiveAddress(next); err != nil {
		return err
	}
	if err := dialer.RejectSelf(mustSpec(next), a.life.Snapshot().URL); err != nil {
		return err
	}
	if err := a.store.Update(func(f *config.File) error {
		f.Proxy = next
		return nil
	}); err != nil {
		return err
	}
	a.life.SyncDirect()
	a.SyncGrok()
	return nil
}

func (a *App) SetImage(baseURL, apiKey, model string) error {
	return a.store.Update(func(f *config.File) error {
		f.Image.BaseURL = strings.TrimSpace(baseURL)
		f.Image.Model = strings.TrimSpace(model)
		if key := strings.TrimSpace(apiKey); key != "" {
			f.Image.APIKey = key
		}
		if f.Image.BaseURL == "" {
			f.Image = config.ImageAPI{}
		}
		return nil
	})
}

func (a *App) SetAutostart(enabled bool) error {
	if _, err := a.boot.Apply(enabled, a.exe); err != nil {
		a.mu.Lock()
		a.bootErr = i18n.Of(err)
		a.mu.Unlock()
		return err
	}
	a.mu.Lock()
	a.bootErr = i18n.Text{}
	a.mu.Unlock()
	return a.store.Update(func(f *config.File) error {
		f.Autostart = enabled
		return nil
	})
}

func (a *App) AddModel(model config.Model) error {
	prepared, err := provider.Prepare(model)
	if err != nil {
		return err
	}
	return a.store.Update(func(f *config.File) error {
		for _, existing := range f.Models {
			if existing.ID == prepared.ID {
				return i18n.E("已经添加过这个模型", "This model has already been added")
			}
		}
		f.Models = append(f.Models, prepared)
		return nil
	})
}

func (a *App) UpdateModel(id string, model config.Model) error {
	if model.ContextWindow < 0 || model.MaxOutputTokens < 0 {
		return i18n.E("token 数不能为负", "Token counts cannot be negative")
	}
	return a.store.Update(func(f *config.File) error {
		for i := range f.Models {
			if f.Models[i].ID != id {
				continue
			}
			prev := f.Models[i]
			if strings.TrimSpace(model.APIKey) == "" {
				if !sameEndpoint(prev, model) {
					return i18n.E("换了服务器地址，请重新填写 API Key", "The server host changed; enter the API key again")
				}
				model.APIKey = prev.APIKey
			}
			prepared, err := provider.Prepare(model)
			if err != nil {
				return err
			}
			if prepared.Type != "openai-chat" {
				prepared.FastSupport = false
			}
			for _, other := range f.Models {
				if other.ID == prepared.ID && other.ID != prev.ID {
					return i18n.E("已经添加过这个模型", "This model has already been added")
				}
			}
			if prev.Type == prepared.Type && prev.BaseURL == prepared.BaseURL && prev.Model == prepared.Model && prev.APIKey == prepared.APIKey {
				prepared.LastTest = prev.LastTest
			}
			f.Models[i] = prepared
			return nil
		}
		return i18n.E("没有这个模型", "No such model")
	})
}

func (a *App) DeleteModel(id string) error {
	return a.store.Update(func(f *config.File) error {
		next := make([]config.Model, 0, len(f.Models))
		found := false
		for _, model := range f.Models {
			if model.ID == id {
				found = true
				continue
			}
			next = append(next, model)
		}
		if !found {
			return i18n.E("没有这个模型", "No such model")
		}
		f.Models = next
		return nil
	})
}

func (a *App) SetModelLimits(id string, contextWindow, maxOutput *int) error {
	if (contextWindow != nil && *contextWindow < 0) || (maxOutput != nil && *maxOutput < 0) {
		return i18n.E("token 数不能为负", "Token counts cannot be negative")
	}
	return a.store.Update(func(f *config.File) error {
		for i := range f.Models {
			if f.Models[i].ID != id {
				continue
			}
			if contextWindow != nil {
				f.Models[i].ContextWindow = *contextWindow
			}
			if maxOutput != nil {
				f.Models[i].MaxOutputTokens = *maxOutput
			}
			return nil
		}
		return i18n.E("没有这个模型", "No such model")
	})
}

func (a *App) SetModelFast(id string, on bool) error {
	return a.store.Update(func(f *config.File) error {
		for i := range f.Models {
			if f.Models[i].ID == id {
				if on && f.Models[i].Type != "openai-chat" {
					return i18n.E("只有 OpenAI 兼容接口可以打开 Fast", "Fast is only available for OpenAI-compatible endpoints")
				}
				f.Models[i].FastSupport = on
				return nil
			}
		}
		return i18n.E("没有这个模型", "No such model")
	})
}

func (a *App) SetModelReasoning(id string, on bool) error {
	return a.store.Update(func(f *config.File) error {
		for i := range f.Models {
			if f.Models[i].ID == id {
				f.Models[i].Reasoning = on
				return nil
			}
		}
		return i18n.E("没有这个模型", "No such model")
	})
}

func (a *App) SetModelProxy(id string, use bool) error {
	return a.store.Update(func(f *config.File) error {
		for i := range f.Models {
			if f.Models[i].ID == id {
				f.Models[i].UseProxy = use
				return nil
			}
		}
		return i18n.E("没有这个模型", "No such model")
	})
}

func (a *App) TestSaved(id string) provider.Result {
	cfg := a.store.Get()
	for _, model := range cfg.Models {
		if model.ID == id {
			result := a.probe(model)
			snap := result.LastTest()
			_ = a.store.Update(func(f *config.File) error {
				for i := range f.Models {
					if f.Models[i].ID == id {
						f.Models[i].LastTest = &snap
						return nil
					}
				}
				return nil
			})
			return result
		}
	}
	return provider.Result{Error: i18n.T("没有这个模型", "No such model")}
}

func (a *App) TestDraft(model config.Model) provider.Result {
	if strings.TrimSpace(model.APIKey) == "" && model.ID != "" {
		for _, existing := range a.store.Get().Models {
			if existing.ID == model.ID {
				// 只在地址和类型都没变时沿用已存的 Key，免得把 Key 发给新填的地址。
				if sameEndpoint(existing, model) {
					model.APIKey = existing.APIKey
				}
				break
			}
		}
	}
	prepared, err := provider.Prepare(model)
	if err != nil {
		return provider.Result{Error: i18n.Of(err)}
	}
	return a.probe(prepared)
}

func (a *App) probe(model config.Model) provider.Result {
	d, err := dialer.ForModel(a.store.Get().Proxy, model.UseProxy)
	if err != nil {
		return provider.Result{Error: i18n.Of(err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	model = provider.ApplyModelsDevDefaults(ctx, model, d, config.DefaultDir())
	result, updated := provider.Probe(ctx, model, d)
	if result.OK {
		_ = a.store.Update(func(f *config.File) error {
			for i := range f.Models {
				if f.Models[i].ID != updated.ID {
					continue
				}
				f.Models[i].Reasoning = updated.Reasoning
				if f.Models[i].ContextWindow <= 0 {
					f.Models[i].ContextWindow = updated.ContextWindow
				}
				if f.Models[i].MaxOutputTokens <= 0 {
					f.Models[i].MaxOutputTokens = updated.MaxOutputTokens
				}
				return nil
			}
			return nil
		})
	}
	return result
}

func (a *App) listenURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listen
}

func mustSpec(p config.Proxy) string {
	spec, on, err := dialer.EffectiveAddress(p)
	if err != nil || !on {
		return ""
	}
	return spec
}

// sameEndpoint 报告两份配置的 Key 会不会发到同一台服务器（协议 + 主机 + 端口，不分大小写）。
// 只改路径或接口类型时沿用旧 Key；换了主机必须重新填写，免得把已存的 Key 发给别处。
func sameEndpoint(a, b config.Model) bool {
	origin := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			return ""
		}
		return strings.ToLower(u.Scheme + "://" + u.Host)
	}
	oa := origin(a.BaseURL)
	return oa != "" && oa == origin(b.BaseURL)
}
