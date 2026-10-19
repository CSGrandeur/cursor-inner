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

// 能力探测的阶段名。页面按 Plan 画进度，各阶段再按 Stage 更新。
const (
	StageTools     = "tools"
	StageReasoning = "reasoning"
	StageImages    = "images"
	StageCache     = "cache"
	StageSpeed     = "speed"
)

// Progress 是一次能力探测的实时进度。Plan 只在第一帧给出，是当前模型实际会跑的阶段；
// Stage 为当前阶段，State 取 start / done / tokens；测速阶段的 Tokens 是已收到的估算 token 数。
type Progress struct {
	Plan   []string
	Stage  string
	State  string
	OK     bool
	Tokens uint64
}

// Probe 做能力探测：工具往返、推理档（400 则自动关）、图片输入、缓存命中，并保留原有测速字段。
// 成功时会按结果改写 m 上的 Reasoning / ContextWindow / MaxOutputTokens（调用方负责写回配置）。
// progress 按阶段报告进度，可以为 nil。
func Probe(ctx context.Context, m config.Model, dial dialer.Func, progress func(Progress)) (Result, config.Model) {
	report := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}
	start := time.Now()
	caps := &config.Capabilities{Family: string(DetectFamily(m))}
	profile := ProfileOf(m)
	if DetectFamily(m) == FamilyDeepSeek {
		if c, o := deepseekVariantLimits(m.Model); c > 0 {
			if m.ContextWindow <= 0 {
				m.ContextWindow = c
			}
			if m.MaxOutputTokens <= 0 {
				m.MaxOutputTokens = o
			}
		}
	}
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

	plan := []string{StageTools}
	if m.Reasoning || profile.ReasoningEcho {
		plan = append(plan, StageReasoning)
	}
	if profile.SupportsImages {
		plan = append(plan, StageImages)
	}
	plan = append(plan, StageCache, StageSpeed)
	report(Progress{Plan: plan})

	report(Progress{Stage: StageTools, State: "start"})
	caps.Tools = probeTools(ctx, m, dial)
	report(Progress{Stage: StageTools, State: "done", OK: caps.Tools})
	if caps.Tools && TextToolActive(m) {
		caps.TextToolMode = true
		m.TextToolMode = true
	}

	if m.Reasoning || profile.ReasoningEcho {
		report(Progress{Stage: StageReasoning, State: "start"})
		m.Reasoning = true
		m.Effort = "low"
		ok := probeReasoning(ctx, m, dial)
		report(Progress{Stage: StageReasoning, State: "done", OK: ok})
		if ok {
			caps.Reasoning = true
		} else {
			m.Reasoning = false
			m.Effort = ""
			caps.Reasoning = false
		}
	}

	if profile.SupportsImages {
		report(Progress{Stage: StageImages, State: "start"})
		caps.Images = probeImages(ctx, m, dial)
		report(Progress{Stage: StageImages, State: "done", OK: caps.Images})
	}
	report(Progress{Stage: StageCache, State: "start"})
	caps.CacheHit = probeCache(ctx, m, dial)
	report(Progress{Stage: StageCache, State: "done", OK: caps.CacheHit})

	report(Progress{Stage: StageSpeed, State: "start"})
	speed := Test(ctx, m, dial, func(tokens uint64) {
		report(Progress{Stage: StageSpeed, State: "tokens", Tokens: tokens})
	})
	report(Progress{Stage: StageSpeed, State: "done", OK: speed.OK})
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

// probeImageBase64 是 256x256 的纯色 PNG。1x1 这类退化图会被部分端点（DeepSeek）判成
// 不支持的图片，探测就会误报不支持看图。
const probeImageBase64 = "iVBORw0KGgoAAAANSUhEUgAAAQAAAAEACAYAAABccqhmAAAAAXNSR0IArs4c6QAAAARnQU1BAACxjwv8YQUAAAAJcEhZcwAADsMAAA7DAcdvqGQAAAQMSURBVHhe7dQxEYBAAAPBF4slZKIBegxckb2ZbSIg55U02/kPknZyANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwH4f9QpKddqKHAAAAAElFTkSuQmCC"

func probeImages(ctx context.Context, m config.Model, dial dialer.Func) bool {
	png, _ := base64.StdEncoding.DecodeString(probeImageBase64)
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
