package app

import (
	"context"
	"strings"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

// DiscoverModels 按表单里的接口地址向上游探测模型列表。apiKey 为空且 id 指向已保存模型时，
// 仅在同一主机下沿用已存密钥（与 TestDraft 一致）。密钥不会出现在返回值里。
func (a *App) DiscoverModels(modelType, baseURL, apiKey string, useProxy bool, reuseID string) ([]string, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	reuseID = strings.TrimSpace(reuseID)
	if apiKey == "" && reuseID != "" {
		for _, existing := range a.store.Get().Models {
			if existing.ID != reuseID {
				continue
			}
			probe := config.Model{Type: modelType, BaseURL: baseURL}
			if sameEndpoint(existing, probe) {
				apiKey = existing.APIKey
			}
			break
		}
	}
	d, err := dialer.ForModel(a.store.Get().Proxy, useProxy)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return provider.DiscoverModels(ctx, provider.DiscoverInput{
		Type:     modelType,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		UseProxy: useProxy,
	}, d)
}
