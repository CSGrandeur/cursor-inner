package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/autostart"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/takeover"
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
	return &App{store: store, life: life, boot: boot, exe: exe}
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

func (a *App) SetTakeover(enabled bool) error {
	if enabled {
		return a.EnableTakeover()
	}
	return a.DisableTakeover()
}

func (a *App) SetProxy(enabled bool, address string) error {
	next := config.Proxy{Enabled: enabled, Address: address}
	if _, _, err := dialer.EffectiveAddress(next); err != nil {
		return err
	}
	if err := dialer.RejectSelf(mustSpec(next), a.life.Snapshot().URL); err != nil {
		return err
	}
	return a.store.Update(func(f *config.File) error {
		f.Proxy = next
		return nil
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return provider.Test(ctx, model, d)
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
