package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/fsutil"
)

type Proxy struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address"`
	// Scope 是 Cursor 和 Grok Bot 流量的代理范围：空或 "official"（默认）只接管官方端点，
	// 其余直连、不经代理；明确写 "global" 才全部走代理。自定义模型的“走代理”开关不看它。
	Scope string `json:"scope,omitempty"`
}

const (
	ScopeGlobal   = "global"
	ScopeOfficial = "official"
)

// ScopeName 返回规范化的代理范围。默认只接管官方流量：空值、旧配置和认不出的值都按“仅官方”，
// 只有明确选了 global 才全局。
func (p Proxy) ScopeName() string {
	if strings.EqualFold(strings.TrimSpace(p.Scope), ScopeGlobal) {
		return ScopeGlobal
	}
	return ScopeOfficial
}

// OfficialOnly 报告是否只让官方端点走代理。
func (p Proxy) OfficialOnly() bool { return p.ScopeName() == ScopeOfficial }

type Model struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Type            string `json:"type"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	UseProxy        bool   `json:"use_proxy"`
	Reasoning       bool   `json:"reasoning,omitempty"`
	FastSupport     bool   `json:"fast,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	// GroupID 指向共享接入配置的分组；空为独立模型。组内模型落盘时只存与组不同的
	// 共享字段（*_set 标记覆盖），读取时 Store.Get 已套回组值，消费方拿到的总是完整配置。
	GroupID      string `json:"group_id,omitempty"`
	TypeSet      bool   `json:"type_set,omitempty"`
	BaseURLSet   bool   `json:"base_url_set,omitempty"`
	APIKeySet    bool   `json:"api_key_set,omitempty"`
	UseProxySet  bool   `json:"use_proxy_set,omitempty"`
	ReasoningSet bool   `json:"reasoning_set,omitempty"`
	// MaxImages / ImageBudgetMB：一次请求里最多带多少张图、图片合计多少 MB；超出时从最旧的工具图片起分批省略。0 用默认值。
	MaxImages      int       `json:"max_images,omitempty"`
	ImageBudgetMB  int       `json:"image_budget_mb,omitempty"`
	PromptCacheKey string    `json:"prompt_cache_key,omitempty"`
	Fallback       []string  `json:"fallback,omitempty"` // 其它已保存模型的 id，仅在尚未流出任何字时切换
	LastTest       *LastTest `json:"last_test,omitempty"`
	// EffortLevels 是 Cursor 思考档菜单的 id，按显示顺序。空则用该模型族已文档化的列表，未知模型用通用 low/medium/high/xhigh/max。
	// 例如 Qwen4 官方为 low、medium、xhigh。旧配置没有该字段，保持兼容。
	EffortLevels []string `json:"effort_levels,omitempty"`
	// DefaultEffort 是菜单默认档，以及客户端选了不支持的旧档时改用的值。空则用家族或通用默认。
	DefaultEffort string `json:"default_effort,omitempty"`
	Effort        string `json:"-"`
	Fast          bool   `json:"-"`
	ImageBaseURL  string `json:"-"`
	ImageAPIKey   string `json:"-"`
	ImageModel    string `json:"-"`
	TextToolMode  bool   `json:"-"` // 本次运行时：端点不支持原生工具，改用文本工具协议
}

// ImageAPI 是设置页选择的出图接口。留空时模型看不到 GenerateImage。
type ImageAPI struct {
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model,omitempty"`
}

type LastTest struct {
	OK                   bool          `json:"ok"`
	At                   string        `json:"at,omitempty"`
	DurationMS           int64         `json:"duration_ms"`
	FirstValidResponseMS *int64        `json:"first_valid_response_ms,omitempty"`
	OutputTokens         uint64        `json:"output_tokens"`
	TokensPerSecond      float64       `json:"tokens_per_second"`
	TokensEstimated      bool          `json:"tokens_estimated"`
	Output               string        `json:"output,omitempty"`
	Error                i18n.Text     `json:"error,omitzero"`
	Capabilities         *Capabilities `json:"capabilities,omitempty"`
}

// Cap status 取值：confirmed（已测出）、unknown（超时/解析失败/未触发，未测出）、
// rejected（服务端明确拒绝该测试参数或测试方式）。空字符串表示旧记录只有 bool。
const (
	CapConfirmed = "confirmed"
	CapUnknown   = "unknown"
	CapRejected  = "rejected"
)

// Capabilities 是能力探测结果；探测成功后可用来自动勾选设置。
// bool 字段仅在已测出时为 true；超时不得写成 false 来表示“不支持”。
// 新增 *Status / *Note 字段对旧配置向后兼容（省略则为旧记录）。
type Capabilities struct {
	Tools           bool      `json:"tools"`
	Reasoning       bool      `json:"reasoning"`
	Images          bool      `json:"images"`
	CacheHit        bool      `json:"cache_hit"`
	Family          string    `json:"family,omitempty"`
	TextToolMode    bool      `json:"text_tool_mode,omitempty"` // 端点不接受原生工具，改走文本工具协议
	Generate        bool      `json:"generate,omitempty"`
	GenerateStatus  string    `json:"generate_status,omitempty"`
	ToolsStatus     string    `json:"tools_status,omitempty"`
	ReasoningStatus string    `json:"reasoning_status,omitempty"`
	ImagesStatus    string    `json:"images_status,omitempty"`
	CacheStatus     string    `json:"cache_status,omitempty"`
	SpeedStatus     string    `json:"speed_status,omitempty"`
	ToolsNote       i18n.Text `json:"tools_note,omitzero"`
	ReasoningNote   i18n.Text `json:"reasoning_note,omitzero"`
	ImagesNote      i18n.Text `json:"images_note,omitzero"`
	CacheNote       i18n.Text `json:"cache_note,omitzero"`
	GenerateNote    i18n.Text `json:"generate_note,omitzero"`
	SpeedNote       i18n.Text `json:"speed_note,omitzero"`
}

// Desktop 是 Windows 上托盘和控制台窗口的设置。零值就是老行为：开着控制台窗口，弹通知。
type Desktop struct {
	// Background 为真时启动不开控制台窗口，只留通知区域图标；需要时从托盘菜单打开。
	Background bool `json:"background,omitempty"`
	// Quiet 为真时不弹气泡通知（更新完成、更新失败、泄漏告警）。图标和提示文字照常变化。
	Quiet bool `json:"quiet,omitempty"`
}

type File struct {
	Takeover            bool     `json:"takeover"`
	TakeoverGrok        bool     `json:"takeover_grok"`
	TakeoverCodex       bool     `json:"takeover_codex"`
	TakeoverAntigravity bool     `json:"takeover_antigravity"`
	Autostart           bool     `json:"autostart"`
	Proxy               Proxy    `json:"proxy"`
	Desktop             Desktop  `json:"desktop,omitzero"`
	Image               ImageAPI `json:"image,omitempty"`
	Groups              []Group  `json:"groups,omitempty"`
	Models              []Model  `json:"models"`
	// QuitNoConfirm 为真时退出不再弹确认框（确认框里勾了“不再询问”）。
	QuitNoConfirm bool `json:"quit_no_confirm,omitempty"`
}

func Default() File {
	return File{
		Takeover:            true,
		TakeoverGrok:        true,
		TakeoverCodex:       true,
		TakeoverAntigravity: true,
		Autostart:           false,
		Proxy:               Proxy{Enabled: true},
		Models:              []Model{},
	}
}

func DefaultDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cursor-inner")
}

// Store 内存里保存解析后的完整配置（组值已套进成员）；落盘时拆回组的覆盖形式。
type Store struct {
	mu   sync.Mutex
	path string
	data File
}

func Load(dir string) (*Store, error) {
	path := filepath.Join(dir, "config.json")
	data := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, err
		}
		data.TakeoverGrok = missingKeyDefaultsOn(raw, "takeover_grok", data.TakeoverGrok)
		data.TakeoverCodex = missingKeyDefaultsOn(raw, "takeover_codex", data.TakeoverCodex)
		data.TakeoverAntigravity = missingKeyDefaultsOn(raw, "takeover_antigravity", data.TakeoverAntigravity)
	}
	if data.Models == nil {
		data.Models = []Model{}
	}
	data.Models = resolveModels(normalizeModels(data.Models, data.Groups), data.Groups)
	s := &Store{path: path, data: data}
	if _, statErr := os.Stat(path); statErr != nil {
		if werr := s.persist(); werr != nil {
			return nil, werr
		}
	}
	return s, nil
}

// Peek 只读地看 dir 里的配置，不创建也不改文件。读不到或解析失败时 ok 为 false。
func Peek(dir string) (File, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return File{}, false
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, false
	}
	f.Models = resolveModels(normalizeModels(f.Models, f.Groups), f.Groups)
	return f, true
}

// missingKeyDefaultsOn 把旧配置里没有的接管开关当成开。显式 false 保持关闭。
func missingKeyDefaultsOn(raw []byte, key string, parsed bool) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return true
	}
	if _, ok := probe[key]; !ok {
		return true
	}
	return parsed
}

// Dir 是这份配置所在的数据目录。不同目录的实例各自记自己是否接管过 Grok / Codex。
func (s *Store) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filepath.Dir(s.path)
}

func (s *Store) Get() File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clone()
}

func (s *Store) Update(fn func(*File) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.clone()
	if err := fn(&next); err != nil {
		return err
	}
	if next.Models == nil {
		next.Models = []Model{}
	}
	// 内存里模型是完整值 + 覆盖位。落盘按位拆回稀疏形式，读回按（可能刚改过的）
	// 组值解析：改组值传导到未覆盖成员，删组把成员物化成独立模型。
	// 应用层改成员共享字段时须同时置对应 *_set 位（见 blankFollowing）。
	next.Models = normalizeModels(next.Models, next.Groups)
	// 先按位拆成落盘形式，再按新组值解析出内存里的完整模型：
	// 改组值传导到未覆盖成员，删组把成员物化成独立模型。
	sparse := make([]Model, len(next.Models))
	for i, m := range next.Models {
		sparse[i] = blankFollowing(m)
	}
	next.Models = resolveModels(sparse, next.Groups)
	s.data = next
	return s.persistSparse()
}

func (s *Store) clone() File {
	out := s.data
	out.Groups = append([]Group(nil), s.data.Groups...)
	for i := range out.Groups {
		out.Groups[i].Profiles = append([]GroupProfile(nil), s.data.Groups[i].Profiles...)
	}
	out.Models = append([]Model(nil), s.data.Models...)
	for i := range out.Models {
		if out.Models[i].LastTest != nil {
			snap := *out.Models[i].LastTest
			if snap.Capabilities != nil {
				cap := *snap.Capabilities
				snap.Capabilities = &cap
			}
			out.Models[i].LastTest = &snap
		}
		if out.Models[i].Fallback != nil {
			out.Models[i].Fallback = append([]string(nil), out.Models[i].Fallback...)
		}
	}
	return out
}

func (s *Store) persist() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return fsutil.WriteFile(s.path, raw)
}

// persistSparse 落盘时把组内成员未覆盖的共享字段清零；内存里的 s.data 保持完整值。
func (s *Store) persistSparse() error {
	full := s.data.Models
	sparse := make([]Model, len(full))
	for i, m := range full {
		sparse[i] = blankFollowing(m)
	}
	s.data.Models = sparse
	err := s.persist()
	s.data.Models = full
	return err
}

func (m Model) KeyHint() string {
	key := strings.TrimSpace(m.APIKey)
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "····"
	}
	return "····" + key[len(key)-4:]
}
