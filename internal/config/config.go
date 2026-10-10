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
}

type Model struct {
	ID              string    `json:"id"`
	DisplayName     string    `json:"display_name"`
	Type            string    `json:"type"`
	BaseURL         string    `json:"base_url"`
	APIKey          string    `json:"api_key"`
	Model           string    `json:"model"`
	UseProxy        bool      `json:"use_proxy"`
	Reasoning       bool      `json:"reasoning,omitempty"`
	FastSupport     bool      `json:"fast,omitempty"`
	ContextWindow   int       `json:"context_window,omitempty"`
	MaxOutputTokens int       `json:"max_output_tokens,omitempty"`
	PromptCacheKey  string    `json:"prompt_cache_key,omitempty"`
	Fallback        []string  `json:"fallback,omitempty"` // 其它已保存模型的 id，仅在尚未流出任何字时切换
	LastTest        *LastTest `json:"last_test,omitempty"`
	Effort          string    `json:"-"`
	Fast            bool      `json:"-"`
	ImageBaseURL    string    `json:"-"`
	ImageAPIKey     string    `json:"-"`
	ImageModel      string    `json:"-"`
	TextToolMode    bool      `json:"-"` // 本次运行时：端点不支持原生工具，改用文本工具协议
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

// Capabilities 是能力探测结果；探测成功后可用来自动勾选设置。
type Capabilities struct {
	Tools        bool   `json:"tools"`
	Reasoning    bool   `json:"reasoning"`
	Images       bool   `json:"images"`
	CacheHit     bool   `json:"cache_hit"`
	Family       string `json:"family,omitempty"`
	TextToolMode bool   `json:"text_tool_mode,omitempty"` // 端点不接受原生工具，改走文本工具协议
}

type File struct {
	Takeover     bool     `json:"takeover"`
	TakeoverGrok bool     `json:"takeover_grok"`
	Autostart    bool     `json:"autostart"`
	Proxy        Proxy    `json:"proxy"`
	Image        ImageAPI `json:"image,omitempty"`
	Models       []Model  `json:"models"`
}

func Default() File {
	return File{
		Takeover:     true,
		TakeoverGrok: true,
		Autostart:    false,
		Proxy:        Proxy{Enabled: true},
		Models:       []Model{},
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
		data.TakeoverGrok = takeoverGrokOrDefault(raw, data.TakeoverGrok)
	}
	if data.Models == nil {
		data.Models = []Model{}
	}
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
	return f, true
}

// takeoverGrokOrDefault 把旧配置里没有的接管 Grok 开关当成开。显式 false 保持关闭。
func takeoverGrokOrDefault(raw []byte, parsed bool) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return true
	}
	if _, ok := probe["takeover_grok"]; !ok {
		return true
	}
	return parsed
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
	s.data = next
	return s.persist()
}

func (s *Store) clone() File {
	out := s.data
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
