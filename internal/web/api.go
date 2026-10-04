package web

import (
	"cursor-inner/internal/autostart"
	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"
)

type ModelView struct {
	ID          string           `json:"id"`
	DisplayName string           `json:"display_name"`
	Type        string           `json:"type"`
	BaseURL     string           `json:"base_url"`
	Model       string           `json:"model"`
	UseProxy    bool             `json:"use_proxy"`
	KeyHint     string           `json:"key_hint"`
	LastTest    *config.LastTest `json:"last_test,omitempty"`
}

type View struct {
	ListenURL      string          `json:"listen_url"`
	Takeover       bool            `json:"takeover"`
	TakeoverActive bool            `json:"takeover_active"`
	MitmURL        string          `json:"mitm_url"`
	CA             string          `json:"ca"`
	CADetail       i18n.Text       `json:"ca_detail"`
	LastError      i18n.Text       `json:"last_error"`
	CatalogWarning i18n.Text       `json:"catalog_warning"`
	Proxy          ProxyView       `json:"proxy"`
	Autostart      autostart.State `json:"autostart"`
	Models         []ModelView     `json:"models"`
}

type ProxyView struct {
	Enabled   bool   `json:"enabled"`
	Address   string `json:"address"`
	Effective bool   `json:"effective"`
}

type Backend interface {
	State() (View, error)
	SetTakeover(enabled bool) error
	SetProxy(enabled bool, address string) error
	SetAutostart(enabled bool) error
	AddModel(model config.Model) error
	DeleteModel(id string) error
	SetModelProxy(id string, use bool) error
	TestDraft(model config.Model) provider.Result
	TestSaved(id string) provider.Result
	Quit() error
}
