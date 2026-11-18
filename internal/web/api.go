package web

import (
	"context"

	"cursor-inner/internal/autostart"
	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"
)

type ModelView struct {
	ID              string           `json:"id"`
	DisplayName     string           `json:"display_name"`
	GroupID         string           `json:"group_id,omitempty"`
	Overrides       []string         `json:"overrides,omitempty"`
	Type            string           `json:"type"`
	BaseURL         string           `json:"base_url"`
	Model           string           `json:"model"`
	UseProxy        bool             `json:"use_proxy"`
	Reasoning       bool             `json:"reasoning"`
	Fast            bool             `json:"fast"`
	ContextWindow   int              `json:"context_window"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	MaxImages       int              `json:"max_images"`
	ImageBudgetMB   int              `json:"image_budget_mb"`
	PromptCacheKey  string           `json:"prompt_cache_key,omitempty"`
	Fallback        []string         `json:"fallback,omitempty"`
	KeyHint         string           `json:"key_hint"`
	LastTest        *config.LastTest `json:"last_test,omitempty"`
}

// GroupView 是分组的共享配置摘要；密钥只给后四位，明文走 /api/groups/{id}/key。
type GroupView struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Type          string             `json:"type"`
	BaseURL       string             `json:"base_url"`
	UseProxy      bool               `json:"use_proxy"`
	Reasoning     bool               `json:"reasoning"`
	KeyHint       string             `json:"key_hint"`
	Models        int                `json:"models"`
	Profiles      []GroupProfileView `json:"profiles,omitempty"`
	ActiveProfile string             `json:"active_profile,omitempty"`
}

// GroupProfileView 是一套套餐的摘要（url+类型+密钥后四位）。
type GroupProfileView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	KeyHint string `json:"key_hint"`
}

type View struct {
	ListenURL           string `json:"listen_url"`
	Takeover            bool   `json:"takeover"`
	TakeoverGrok        bool   `json:"takeover_grok"`
	TakeoverCodex       bool   `json:"takeover_codex"`
	TakeoverAntigravity bool   `json:"takeover_antigravity"`
	TakeoverActive      bool   `json:"takeover_active"`
	// TakeoverState 是三态：off（没要求接管）/ pending（还没定论）/ active / error（明确失败）。
	// 页面只在 error 时标红，pending 显示中性的过程文字并继续轮询。Reason 见 takeover.Reason*，Since 是进入该状态的时刻（RFC3339）。
	TakeoverState  string          `json:"takeover_state"`
	TakeoverReason string          `json:"takeover_reason,omitempty"`
	TakeoverSince  string          `json:"takeover_since,omitempty"`
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
	Groups         []GroupView     `json:"groups"`
	Models         []ModelView     `json:"models"`
	// AccountOrder 按最近一次使用时间降序排列账号子标签（"cursor" / "antigravity"）。
	AccountOrder []string `json:"account_order,omitempty"`
	// CursorAccounts 与 AntiAccounts 是两款 IDE 的账号列表；不支持的一侧为 null，空数组保留 []。
	CursorAccounts     []AntiAccountView `json:"cursor_accounts"`
	CursorAccountError i18n.Text         `json:"cursor_account_error,omitzero"`
	AntiAccounts       []AntiAccountView `json:"anti_accounts"`
	AntiAccountError   i18n.Text         `json:"anti_account_error,omitzero"`
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
	// Wait 是等代理开始监听的进度：""（不在等）/ "waiting"（还没定论，中性）/ "down"（超时仍没监听）。
	Wait      string `json:"wait,omitempty"`
	WaitAddr  string `json:"wait_addr,omitempty"`
	WaitSince string `json:"wait_since,omitempty"`
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

// AntiQuotaView 是单个模型家族的剩余配额摘要。
type AntiQuotaView struct {
	Label   string `json:"label"`
	Percent int    `json:"percent"`
	ResetAt int64  `json:"reset_at,omitempty"`
	Window  string `json:"window,omitempty"`
}

// AntiAccountView 是反重力账号摘要（不含 token）。
type AntiAccountView struct {
	ID        string          `json:"id"`
	Email     string          `json:"email"`
	Name      string          `json:"name,omitempty"`
	Tier      string          `json:"tier,omitempty"`
	Quotas    []AntiQuotaView `json:"quotas,omitempty"`
	Status    string          `json:"status,omitempty"`
	CreatedAt int64           `json:"created_at"`
	LastUsed  int64           `json:"last_used"`
	Current   bool            `json:"current"`
	Expiry    int64           `json:"expiry,omitempty"`
}

// AntiLoginState 是一次 Google 授权的进度：running（等浏览器回调）、done（成或败）。
type AntiLoginState struct {
	Running bool             `json:"running"`
	Done    bool             `json:"done"`
	Account *AntiAccountView `json:"account,omitempty"`
	Error   i18n.Text        `json:"error,omitzero"`
}

// AntiImportResult 是批量导入里一条的结果。
type AntiImportResult struct {
	Email string    `json:"email,omitempty"`
	OK    bool      `json:"ok"`
	Error i18n.Text `json:"error,omitzero"`
}

// AntiAccountBackend 是反重力账号管理的可选后端；实现了才挂 /api/antigravity/accounts 路由。
type AntiAccountBackend interface {
	AntiAccountList() []AntiAccountView
	AntiAccountLoginStart() (string, error)
	AntiAccountLoginState() AntiLoginState
	AntiAccountLoginCancel()
	AntiAccountImport(ctx context.Context, refreshToken string) (AntiAccountView, error)
	AntiAccountImportCurrent(ctx context.Context) (AntiAccountView, error)
	AntiAccountImportJSON(ctx context.Context, raw []byte) ([]AntiImportResult, error)
	AntiAccountExportJSON() ([]byte, error)
	AntiAccountRemove(id string) error
	AntiAccountRefresh(ctx context.Context, id string) (AntiAccountView, error)
	AntiAccountRefreshAll(ctx context.Context) (okCount, failCount int, firstErr error)
	AntiAccountSwitch(ctx context.Context, id string) error
}

// CursorAccountBackend 是 Cursor 账号管理的可选后端；实现了才挂 /api/cursor/accounts 路由。
type CursorAccountBackend interface {
	CursorAccountList() []AntiAccountView
	CursorAccountLoginStart() (string, error)
	CursorAccountLoginState() AntiLoginState
	CursorAccountLoginCancel()
	CursorAccountImport(ctx context.Context, rawToken string) (AntiAccountView, error)
	CursorAccountImportCurrent(ctx context.Context) (AntiAccountView, error)
	CursorAccountImportJSON(ctx context.Context, raw []byte) ([]AntiImportResult, error)
	CursorAccountExportJSON() ([]byte, error)
	CursorAccountRemove(id string) error
	CursorAccountRefresh(ctx context.Context, id string) (AntiAccountView, error)
	CursorAccountRefreshAll(ctx context.Context) (okCount, failCount int, firstErr error)
	CursorAccountSwitch(ctx context.Context, id string) error
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
	AddGroup(group config.Group) error
	UpdateGroup(id string, group config.Group) error
	DeleteGroup(id string) error
	SetGroupProfile(id, profileID string) error
	// GroupKey 返回分组密钥明文，只供本机配置页复制；其它接口只给 KeyHint。
	GroupKey(id string) (string, error)
	// ResetModelOverrides 把组内成员的指定字段恢复为跟随分组。
	ResetModelOverrides(id string, fields []string) error
	// ModelKey 返回单个模型保存的密钥明文，只供本机配置页复制；其它接口只给 KeyHint。
	ModelKey(id string) (string, error)
	SetModelProxy(id string, use bool) error
	SetModelReasoning(id string, on bool) error
	SetModelFast(id string, on bool) error
	SetModelLimits(id string, contextWindow, maxOutput *int) error
	// DiscoverModels probes upstream provider model ids; never log or return the raw API key.
	DiscoverModels(modelType, baseURL, apiKey string, useProxy bool, reuseID, groupID string) ([]string, error)
	TestDraft(ctx context.Context, model config.Model, progress func(provider.Progress)) provider.Result
	TestSaved(ctx context.Context, id string, progress func(provider.Progress)) provider.Result
	Quit() error
	QuitPlan() QuitPlan
	QuitConfirmed(dontAsk bool) error
	RestoreApps(dismiss bool) error
	OpenCursor() error
	OpenGrok() error
	OpenCodex() error
	OpenAntigravity() error
	SetStrictEgress(enabled bool) error
	SetTunnel(enabled bool) error
	OpenLogs() error
	RunLeakCheck() error
}
