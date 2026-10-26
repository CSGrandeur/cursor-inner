package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
)

// DiscoverInput 是从上游接口地址拉取模型列表的入参。不含已配置列表。
type DiscoverInput struct {
	Type     string
	BaseURL  string
	APIKey   string
	UseProxy bool
}

// DiscoverModels 请求上游的 models 列表（OpenAI 兼容或 Anthropic），返回排序去重后的 id。
// 密钥只用于 Authorization / x-api-key，不会写入返回值；调用方勿把完整密钥打进日志。
func DiscoverModels(ctx context.Context, in DiscoverInput, dial dialer.Func) ([]string, error) {
	in.Type = strings.TrimSpace(in.Type)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	in.APIKey = strings.TrimSpace(in.APIKey)
	if in.BaseURL == "" {
		return nil, i18n.E("请先填写接口地址", "Enter an endpoint URL first")
	}
	if in.APIKey == "" {
		return nil, i18n.E("请先填写密钥", "Enter an API key first")
	}
	switch in.Type {
	case "openai-chat", "openai-responses", "anthropic":
	default:
		return nil, i18n.E("接口类型只支持 openai-chat、openai-responses 和 anthropic", "Endpoint type must be openai-chat, openai-responses or anthropic")
	}
	urls, err := discoveryURLs(in.BaseURL)
	if err != nil {
		return nil, err
	}
	if dial == nil {
		dial = dialer.Direct()
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext:       dial,
		Proxy:             nil,
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}, Timeout: 30 * time.Second}

	var last error
	for _, u := range urls {
		models, err := discoverAt(ctx, client, in.Type, u, in.APIKey)
		if err == nil {
			sort.Strings(models)
			return uniqueSorted(models), nil
		}
		last = err
	}
	if last == nil {
		last = i18n.E("没有可用的模型探测地址", "No model discovery URL available")
	}
	return nil, last
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(in))
	prev := ""
	for i, s := range in {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}

// discoveryURLs 按 cursor-byok 的规则从接口地址推出 /v1/models（及回退 /models）。
func discoveryURLs(baseURL string) ([]string, error) {
	configured, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || configured.Host == "" || (configured.Scheme != "http" && configured.Scheme != "https") {
		return nil, i18n.E("接口地址需要以 http 或 https 开头", "Endpoint URL must start with http or https")
	}
	path := strings.TrimRight(configured.EscapedPath(), "/")
	tail := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		tail = path[i+1:]
	}
	lowTail := strings.ToLower(tail)
	if lowTail == "model" || lowTail == "models" {
		configured.RawQuery = ""
		configured.Fragment = ""
		return []string{configured.String()}, nil
	}

	primary, err := discoveryURL(baseURL)
	if err != nil {
		return nil, err
	}
	versioned := len(tail) > 1 && tail[0] == 'v' && isDigits(tail[1:])
	complete := hasSuffixFold(path, "/chat/completions") ||
		hasSuffixFold(path, "/responses") ||
		hasSuffixFold(path, "/messages") ||
		hasSuffixFold(path, "/completions")
	if versioned || complete {
		return []string{primary}, nil
	}
	u, err := url.Parse(primary)
	if err != nil {
		return []string{primary}, nil
	}
	prefix, ok := strings.CutSuffix(u.EscapedPath(), "/v1/models")
	if !ok {
		return []string{primary}, nil
	}
	fallback := *u
	fallback.Path = prefix + "/models"
	fallback.RawPath = ""
	return []string{primary, fallback.String()}, nil
}

func discoveryURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", i18n.E("接口地址需要以 http 或 https 开头", "Endpoint URL must start with http or https")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	tail := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		tail = path[i+1:]
	}
	versioned := len(tail) > 1 && tail[0] == 'v' && isDigits(tail[1:])
	var newPath string
	switch {
	case hasSuffixFold(path, "/chat/completions"):
		newPath = path[:len(path)-len("/chat/completions")] + "/models"
	case hasSuffixFold(path, "/responses"):
		newPath = path[:len(path)-len("/responses")] + "/models"
	case hasSuffixFold(path, "/messages"):
		newPath = path[:len(path)-len("/messages")] + "/models"
	case hasSuffixFold(path, "/completions"):
		newPath = path[:len(path)-len("/completions")] + "/models"
	case path == "" || path == "/":
		newPath = "/v1/models"
	case versioned:
		newPath = path + "/models"
	default:
		newPath = path + "/v1/models"
	}
	u.Path = newPath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func hasSuffixFold(path, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(path), strings.ToLower(suffix))
}

func discoverAt(ctx context.Context, client *http.Client, typ, rawURL, apiKey string) ([]string, error) {
	if typ == "anthropic" {
		return anthropicModelsAt(ctx, client, rawURL, apiKey)
	}
	return openaiModelsAt(ctx, client, rawURL, apiKey)
}

func openaiModelsAt(ctx context.Context, client *http.Client, rawURL, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, i18n.E("上游返回的不是 JSON", "Upstream did not return JSON")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, i18n.Ef("探测模型失败（%d）：%s", "Model discovery failed (%d): %s", resp.StatusCode, excerpt(body))
	}
	return modelIDs(payloadData(payload)), nil
}

func anthropicModelsAt(ctx context.Context, client *http.Client, rawURL, apiKey string) ([]string, error) {
	found := map[string]struct{}{}
	afterID := ""
	for {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("limit", "100")
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, i18n.E("上游返回的不是 JSON", "Upstream did not return JSON")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, i18n.Ef("探测模型失败（%d）：%s", "Model discovery failed (%d): %s", resp.StatusCode, excerpt(body))
		}
		for _, id := range modelIDs(payloadData(payload)) {
			found[id] = struct{}{}
		}
		hasMore, _ := payload["has_more"].(bool)
		if !hasMore {
			break
		}
		next, _ := payload["last_id"].(string)
		if strings.TrimSpace(next) == "" {
			return nil, i18n.E("Anthropic 返回 has_more 但没有 last_id", "Anthropic model response has_more without last_id")
		}
		afterID = next
	}
	out := make([]string, 0, len(found))
	for id := range found {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func payloadData(payload any) any {
	if m, ok := payload.(map[string]any); ok {
		if data, ok := m["data"]; ok {
			return data
		}
	}
	return payload
}

func modelIDs(value any) []string {
	arr, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				out = append(out, s)
			}
		case map[string]any:
			id, _ := v["id"].(string)
			if strings.TrimSpace(id) == "" {
				id, _ = v["name"].(string)
			}
			if s := strings.TrimSpace(id); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
