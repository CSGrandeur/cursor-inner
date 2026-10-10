package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

const modelsDevURL = "https://models.dev/api.json"

var (
	modelsDevMu    sync.Mutex
	modelsDevCache map[string]modelsDevEntry
	modelsDevAt    time.Time
)

type modelsDevEntry struct {
	ID              string `json:"id"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Reasoning       bool   `json:"reasoning"`
	Tools           bool   `json:"tools"`
	Images          bool   `json:"images"`
}

// ApplyModelsDevDefaults 用本地缓存（必要时经出站代理刷新）填空的上下文窗口与最大输出。
// 失败时静默保留原值；离线时只读缓存。
func ApplyModelsDevDefaults(ctx context.Context, m config.Model, dial dialer.Func, cacheDir string) config.Model {
	entry, ok := lookupModelsDev(ctx, m, dial, cacheDir)
	if !ok {
		return m
	}
	if m.ContextWindow <= 0 && entry.ContextWindow > 0 {
		m.ContextWindow = entry.ContextWindow
	}
	if m.MaxOutputTokens <= 0 && entry.MaxOutputTokens > 0 {
		m.MaxOutputTokens = entry.MaxOutputTokens
	}
	if !m.Reasoning && entry.Reasoning {
		m.Reasoning = true
	}
	return m
}

func lookupModelsDev(ctx context.Context, m config.Model, dial dialer.Func, cacheDir string) (modelsDevEntry, bool) {
	key := strings.ToLower(strings.TrimSpace(m.Model))
	if key == "" {
		return modelsDevEntry{}, false
	}
	modelsDevMu.Lock()
	defer modelsDevMu.Unlock()
	if modelsDevCache == nil {
		modelsDevCache = loadModelsDevFile(cacheDir)
	}
	if e, ok := modelsDevCache[key]; ok {
		return e, true
	}
	if time.Since(modelsDevAt) < 24*time.Hour && len(modelsDevCache) > 0 {
		return modelsDevEntry{}, false
	}
	if dial == nil {
		return modelsDevEntry{}, false
	}
	if err := refreshModelsDev(ctx, dial, cacheDir); err != nil {
		return modelsDevEntry{}, false
	}
	e, ok := modelsDevCache[key]
	return e, ok
}

func loadModelsDevFile(cacheDir string) map[string]modelsDevEntry {
	out := map[string]modelsDevEntry{}
	if cacheDir == "" {
		return out
	}
	raw, err := os.ReadFile(filepath.Join(cacheDir, "models.dev.json"))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func refreshModelsDev(ctx context.Context, dial dialer.Func, cacheDir string) error {
	client := &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	// models.dev 顶层形状不固定：尽量从任意嵌套里抽 id / limit 字段。
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	next := map[string]modelsDevEntry{}
	walkModelsDev(raw, next)
	if len(next) == 0 {
		return nil
	}
	modelsDevCache = next
	modelsDevAt = time.Now()
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o700)
		if encoded, err := json.MarshalIndent(next, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(cacheDir, "models.dev.json"), append(encoded, '\n'), 0o600)
		}
	}
	return nil
}

func walkModelsDev(node any, out map[string]modelsDevEntry) {
	switch v := node.(type) {
	case map[string]any:
		id, _ := v["id"].(string)
		if id == "" {
			id, _ = v["model"].(string)
		}
		if id != "" {
			e := modelsDevEntry{ID: id}
			if n, ok := asInt(v["context_window"]); ok {
				e.ContextWindow = n
			} else if n, ok := asInt(v["context"]); ok {
				e.ContextWindow = n
			}
			if n, ok := asInt(v["max_output_tokens"]); ok {
				e.MaxOutputTokens = n
			} else if n, ok := asInt(v["max_tokens"]); ok {
				e.MaxOutputTokens = n
			}
			if b, ok := v["reasoning"].(bool); ok {
				e.Reasoning = b
			}
			if b, ok := v["tool_call"].(bool); ok {
				e.Tools = b
			}
			if b, ok := v["tools"].(bool); ok {
				e.Tools = b
			}
			if b, ok := v["vision"].(bool); ok {
				e.Images = b
			}
			out[strings.ToLower(id)] = e
		}
		for _, child := range v {
			walkModelsDev(child, out)
		}
	case []any:
		for _, child := range v {
			walkModelsDev(child, out)
		}
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
