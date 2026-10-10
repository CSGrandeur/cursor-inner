package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
)

// Probe 做能力探测：工具往返、推理档（400 则自动关）、图片输入、缓存命中，并保留原有测速字段。
// 成功时会按结果改写 m 上的 Reasoning / ContextWindow / MaxOutputTokens（调用方负责写回配置）。
func Probe(ctx context.Context, m config.Model, dial dialer.Func) (Result, config.Model) {
	start := time.Now()
	caps := &config.Capabilities{Family: string(DetectFamily(m))}
	profile := ProfileOf(m)
	if DetectFamily(m) == FamilyQwen {
		if c, o := qwenVariantLimits(m.Model); c > 0 {
			if m.ContextWindow <= 0 {
				m.ContextWindow = c
			}
			if m.MaxOutputTokens <= 0 {
				m.MaxOutputTokens = o
			}
		}
	}
	if m.ContextWindow <= 0 && profile.DefaultContext > 0 {
		m.ContextWindow = profile.DefaultContext
	}
	if m.MaxOutputTokens <= 0 && profile.DefaultMaxOut > 0 {
		m.MaxOutputTokens = profile.DefaultMaxOut
	}

	caps.Tools = probeTools(ctx, m, dial)
	if caps.Tools && TextToolActive(m) {
		caps.TextToolMode = true
		m.TextToolMode = true
	}

	if m.Reasoning || profile.ReasoningEcho {
		m.Reasoning = true
		m.Effort = "low"
		if probeReasoning(ctx, m, dial) {
			caps.Reasoning = true
		} else {
			m.Reasoning = false
			m.Effort = ""
			caps.Reasoning = false
		}
	}

	if profile.SupportsImages {
		caps.Images = probeImages(ctx, m, dial)
	}
	caps.CacheHit = probeCache(ctx, m, dial)

	speed := Test(ctx, m, dial)
	result := speed
	result.Capabilities = caps
	if !result.OK && caps.Tools {
		result.OK = true
		result.Error = i18n.Text{}
	}
	if result.At == "" {
		result.At = start.UTC().Format(time.RFC3339)
	}
	return result, m
}

func probeTools(ctx context.Context, m config.Model, dial dialer.Func) bool {
	catalog := []Tool{{
		Name:        "ping_probe",
		Description: "Return pong",
		Parameters:  []byte(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`),
	}}
	messages := []Message{{Role: "user", Content: "Call ping_probe with x=hi. Do not answer in plain text."}}
	msg, err := Chat(ctx, m, dial, "You are a tool-calling probe.", messages, catalog, nil, nil)
	if err != nil || len(msg.ToolCalls) == 0 {
		return false
	}
	for _, c := range msg.ToolCalls {
		if strings.EqualFold(c.Name, "ping_probe") || strings.Contains(strings.ToLower(c.Name), "ping") {
			return true
		}
	}
	return true
}

func probeReasoning(ctx context.Context, m config.Model, dial dialer.Func) bool {
	messages := []Message{{Role: "user", Content: "Reply with exactly OK."}}
	_, err := Chat(ctx, m, dial, "", messages, nil, nil, nil)
	if err == nil {
		return true
	}
	var api *APIError
	if errors.As(err, &api) && (api.Kind == KindBadRequest || api.Status == 400) {
		return false
	}
	return m.Reasoning
}

func probeImages(ctx context.Context, m config.Model, dial dialer.Func) bool {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	messages := []Message{{
		Role:    "user",
		Content: "Describe the image in one word.",
		Images:  []Image{{MIME: "image/png", Data: png}},
	}}
	msg, err := Chat(ctx, m, dial, "", messages, nil, nil, nil)
	return err == nil && strings.TrimSpace(msg.Content) != ""
}

func probeCache(ctx context.Context, m config.Model, dial dialer.Func) bool {
	sys := strings.Repeat("Cache prefix probe. ", 40)
	messages := []Message{{Role: "user", Content: "Reply with exactly OK."}}
	if _, err := Chat(ctx, m, dial, sys, messages, nil, nil, nil); err != nil {
		return false
	}
	second, err := Chat(ctx, m, dial, sys, messages, nil, nil, nil)
	if err != nil {
		return false
	}
	return second.CacheTokens > 0
}
