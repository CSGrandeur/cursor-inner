package web

import (
	"cursor-inner/internal/autostart"
	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"
)

type ModelView struct {
	ID              string           `json:"id"`
	DisplayName     string           `json:"display_name"`
	Type            string           `json:"type"`
	BaseURL         string           `json:"base_url"`
	Model           string           `json:"model"`
	UseProxy        bool             `json:"use_proxy"`
	Reasoning       bool             `json:"reasoning"`
	Fast            bool             `json:"fast"`
	ContextWindow   int              `json:"context_window"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	PromptCacheKey  string           `json:"prompt_cache_key,omitempty"`
	Fallback        []string         `json:"fallback,omitempty"`
	KeyHint         string           `json:"key_hint"`
	LastTest        *config.LastTest `json:"last_test,omitempty"`
}

type View struct {
	ListenURL      string          `json:"listen_url"`
	Takeover       bool            `json:"takeover"`
	TakeoverGrok   bool            `json:"takeover_grok"`
	TakeoverCodex  bool            `json:"takeover_codex"`
	TakeoverActive bool            `json:"takeover_active"`
	MitmURL        string          `json:"mitm_url"`
	CA             string          `json:"ca"`
	CADetail       i18n.Text       `json:"ca_detail"`
	LastError      i18n.Text       `json:"last_error"`
	CatalogWarning i18n.Text       `json:"catalog_warning"`
	Proxy          ProxyView       `json:"proxy"`
	Image          ImageView       `json:"image"`
	Autostart      autostart.State `json:"autostart"`
	Tunnel         TunnelView      `json:"tunnel"`
	Desktop        DesktopView     `json:"desktop"`
	Models         []ModelView     `json:"models"`
}

type ImageView struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	KeyHint string `json:"key_hint"`
}

type ProxyView struct {
	Enabled   bool   `json:"enabled"`
	Address   string `json:"address"`
	Effective bool   `json:"effective"`
	Scope     string `json:"scope"`   // global / official
	Bridged   bool   `json:"bridged"` // 地址本身就要经本机代理桥（socks / 带认证）：切换范围时 Grok Bot 不用重开
}

// DesktopView 是托盘和控制台窗口的设置。Tray 为假（非 Windows，或托盘没挂上）时配置页不显示这两行。
type DesktopView struct {
	Tray       bool `json:"tray"`
	Background bool `json:"background"`
	Notify     bool `json:"notify"`
}

type TunnelView struct {
	Status int       `json:"status"`
	Detail i18n.Text `json:"detail"`
}

type Backend interface {
	State() (View, error)
	SetTakeover(target string, enabled bool) error
	SetProxy(enabled bool, address string) error
	SetProxyScope(scope string) error
	SetImage(baseURL, apiKey, model string) error
	SetAutostart(enabled bool) error
	// SetDesktop 改托盘相关设置；nil 表示这一项不改。
	SetDesktop(background, notify *bool) error
	AddModel(model config.Model) error
	UpdateModel(id string, model config.Model) error
	DeleteModel(id string) error
	SetModelProxy(id string, use bool) error
	SetModelReasoning(id string, on bool) error
	SetModelFast(id string, on bool) error
	SetModelLimits(id string, contextWindow, maxOutput *int) error
	TestDraft(model config.Model) provider.Result
	TestSaved(id string) provider.Result
	Quit() error
	OpenCursor() error
	OpenGrok() error
	OpenCodex() error
	SetStrictEgress(enabled bool) error
	SetTunnel(enabled bool) error
	OpenLogs() error
	RunLeakCheck() error
}
