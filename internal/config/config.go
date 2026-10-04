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
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Type        string    `json:"type"`
	BaseURL     string    `json:"base_url"`
	APIKey      string    `json:"api_key"`
	Model       string    `json:"model"`
	UseProxy    bool      `json:"use_proxy"`
	LastTest    *LastTest `json:"last_test,omitempty"`
}

type LastTest struct {
	OK                   bool      `json:"ok"`
	At                   string    `json:"at,omitempty"`
	DurationMS           int64     `json:"duration_ms"`
	FirstValidResponseMS *int64    `json:"first_valid_response_ms,omitempty"`
	OutputTokens         uint64    `json:"output_tokens"`
	TokensPerSecond      float64   `json:"tokens_per_second"`
	TokensEstimated      bool      `json:"tokens_estimated"`
	Output               string    `json:"output,omitempty"`
	Error                i18n.Text `json:"error,omitzero"`
}

type File struct {
	Takeover  bool    `json:"takeover"`
	Autostart bool    `json:"autostart"`
	Proxy     Proxy   `json:"proxy"`
	Models    []Model `json:"models"`
}

func Default() File {
	return File{
		Takeover:  true,
		Autostart: false,
		Proxy:     Proxy{Enabled: true},
		Models:    []Model{},
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
			out.Models[i].LastTest = &snap
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
