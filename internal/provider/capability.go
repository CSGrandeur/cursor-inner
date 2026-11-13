package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/logx"
)

// 能力探测的阶段名。页面按 Plan 画进度，各阶段再按 Stage 更新。
const (
	StageGenerate  = "generate"
	StageTools     = "tools"
	StageReasoning = "reasoning"
	StageImages    = "images"
	StageCache     = "cache"
	StageSpeed     = "speed"
)

// 总预算仍有限（90s），但各阶段用独立子超时，避免第一项耗尽共享 ctx 让后面假失败。
// 不只靠加长总超时来兼容慢首包。
const (
	probeTotalBudget     = 90 * time.Second
	probeGenerateBudget  = 25 * time.Second
	probeToolsBudget     = 20 * time.Second
	probeReasoningBudget = 15 * time.Second
	probeImagesBudget    = 15 * time.Second
	probeCacheBudget     = 15 * time.Second
	probeSpeedBudget     = 15 * time.Second
	probeContrastBudget  = 15 * time.Second
	probeCacheMinRemain  = 8 * time.Second
	// 官方：Qwen4 的 max_output_tokens 是思考+输出合计，最小 16。
	// 探测用 8192 作为有界共享预算；2048 在默认 xhigh 思考下可能把预算用尽造成假阴性。
	// 用户手填的数十万上限仍不会原样发出。
	probeMaxOutputTokens = 8192
)

// Progress 是一次能力探测的实时进度。Plan 只在第一帧给出，是当前模型实际会跑的阶段；
// Stage 为当前阶段，State 取 start / waiting_headers / stream / tool_args / done / tokens / skipped；
// Info 是面向用户的实际进展，不得虚构。
type Progress struct {
	Plan   []string  `json:"plan,omitempty"`
	Stage  string    `json:"stage,omitempty"`
	State  string    `json:"state,omitempty"`
	OK     bool      `json:"ok,omitempty"`
	Tokens uint64    `json:"tokens,omitempty"`
	Info   i18n.Text `json:"info,omitzero"`
}

func stageCtx(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if parent.Err() != nil {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
	remain := budget
	if dl, ok := parent.Deadline(); ok {
		r := time.Until(dl)
		if r <= 0 {
			ctx, cancel := context.WithCancel(parent)
			cancel()
			return ctx, cancel
		}
		if r < remain {
			remain = r
		}
	}
	return context.WithTimeout(parent, remain)
}

// MergeCapabilities 超时/未测出不得把此前已确认的能力覆盖成 false。
// 服务端明确拒绝该测试方式时允许更新为 rejected。
func MergeCapabilities(prev, next *config.Capabilities) *config.Capabilities {
	if next == nil {
		return prev
	}
	if prev == nil {
		return next
	}
	out := *next
	protect := func(status *string, flag *bool, prevStatus string, prevFlag bool) {
		if *status == config.CapUnknown || *status == "" {
			if prevStatus == config.CapConfirmed || prevFlag {
				*status = config.CapConfirmed
				*flag = true
			}
		}
	}
	protect(&out.GenerateStatus, &out.Generate, prev.GenerateStatus, prev.Generate)
	protect(&out.ToolsStatus, &out.Tools, prev.ToolsStatus, prev.Tools)
	protect(&out.ReasoningStatus, &out.Reasoning, prev.ReasoningStatus, prev.Reasoning)
	protect(&out.ImagesStatus, &out.Images, prev.ImagesStatus, prev.Images)
	if (out.CacheStatus == config.CapUnknown || out.CacheStatus == "") && (prev.CacheStatus == config.CapConfirmed || prev.CacheHit) {
		out.CacheStatus = config.CapConfirmed
		out.CacheHit = true
	}
	if prev.TextToolMode && out.Tools {
		out.TextToolMode = true
	}
	return &out
}

func classifyProbeErr(err error) (string, i18n.Text) {
	if err == nil {
		return config.CapUnknown, i18n.T("未测出", "Not determined")
	}
	if errors.Is(err, context.Canceled) {
		return config.CapUnknown, i18n.T("测试已取消，未测出", "Test canceled; not determined")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return config.CapUnknown, i18n.T("等待响应头超时，未测出", "Timed out waiting for response headers; not determined")
	}
	var api *APIError
	if errors.As(err, &api) {
		switch api.Kind {
		case KindAuth:
			return config.CapUnknown, i18n.Tf("接口鉴权失败（%s），未测出", "Authentication failed (%s); not determined", Explain(err))
		case KindRateLimit:
			return config.CapUnknown, i18n.T("429 限流，未测出", "429 rate limited; not determined")
		case KindTimeout:
			return config.CapUnknown, i18n.T("等待响应头超时，未测出", "Timed out waiting for response headers; not determined")
		case KindServer:
			return config.CapUnknown, i18n.Tf("上游服务错误（%s），未测出", "Upstream error (%s); not determined", Explain(err))
		case KindBadRequest:
			return config.CapRejected, i18n.Tf("该接口拒绝此参数或测试方式：%s", "The endpoint rejected this parameter or test method: %s", Explain(err))
		}
	}
	return config.CapUnknown, i18n.Tf("测试异常：%s", "Test error: %s", Explain(err))
}

func defaultProbeOpts(m config.Model, withTools, dropEffort bool) probeOpts {
	o := probeOpts{OmitInclude: true}
	if !dropEffort {
		o.ReasoningEffort = probeReasoningEffort(m.Model)
	}
	if withTools {
		o.ToolChoice = "required"
		if m.Type == "openai-responses" {
			o.FlatTools = true
		}
	}
	return o
}

// Probe 做能力探测：先基本生成，再按独立预算测工具、推理、图片、缓存和测速。
// 成功时会按结果改写 m 上的 ContextWindow / MaxOutputTokens（仅当用户未手填）；
// Reasoning 仅在服务端明确拒绝推理参数时才改为 false，超时不得关掉用户已开的推理。
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
	if DetectFamily(m) == FamilyKimi {
		if c, o := kimiVariantLimits(m.Model); c > 0 {
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

	plan := []string{StageGenerate, StageTools}
	if m.Reasoning || profile.ReasoningEcho {
		plan = append(plan, StageReasoning)
	}
	if profile.SupportsImages {
		plan = append(plan, StageImages)
	}
	plan = append(plan, StageCache, StageSpeed)
	report(Progress{Plan: plan})

	run := &probeRun{m: m, dial: dial, report: report}
	result := Result{At: start.UTC().Format(time.RFC3339), Capabilities: caps}

	genOK, genErr := run.probeGenerate(ctx, caps)
	if !genOK {
		skipRest(report, plan, StageGenerate, i18n.T("因基础生成未完成，未测此项", "Skipped because basic generation did not finish"))
		markUnknownRemaining(caps, plan, StageGenerate, i18n.T("因基础生成未完成，未测此项", "Skipped because basic generation did not finish"))
		if genErr != nil {
			result.Error = noteOr(caps.GenerateNote, i18n.Of(genErr))
		} else if !caps.GenerateNote.IsZero() {
			result.Error = caps.GenerateNote
		} else {
			result.Error = i18n.T("基础生成未测出", "Basic generation was not determined")
		}
		result.DurationMS = time.Since(start).Milliseconds()
		result.OK = false
		return result, m
	}

	run.probeTools(ctx, caps)
	if caps.Tools && TextToolActive(m) {
		caps.TextToolMode = true
		m.TextToolMode = true
	}

	if m.Reasoning || profile.ReasoningEcho {
		ok := run.probeReasoning(ctx, caps)
		if caps.ReasoningStatus == config.CapRejected {
			m.Reasoning = false
			m.Effort = ""
		} else if ok {
			// 已测出推理请求可被接受，不改用户已开的推理开关。
			m.Reasoning = true
		}
	}

	if profile.SupportsImages {
		run.probeImages(ctx, caps)
	}
	run.probeCache(ctx, caps)

	report(Progress{Stage: StageSpeed, State: "start", Info: i18n.T("测速：流式计数 token", "Speed: counting streamed tokens")})
	sctx, scancel := stageCtx(ctx, probeSpeedBudget)
	speed := Test(withProbeOpts(sctx, defaultProbeOpts(m, false, run.dropEffort)), m, dial, func(tokens uint64) {
		report(Progress{Stage: StageSpeed, State: "tokens", Tokens: tokens})
	})
	scancel()
	if speed.OK {
		caps.SpeedStatus = config.CapConfirmed
		result = speed
		result.Capabilities = caps
	} else {
		st, note := classifyProbeErr(errorFromText(speed.Error))
		if speed.Error.IsZero() {
			st, note = config.CapUnknown, i18n.T("测速未得到有效输出，未测出", "Speed test got no valid output; not determined")
		}
		caps.SpeedStatus = st
		caps.SpeedNote = note
		report(Progress{Stage: StageSpeed, State: "done", Info: note})
		// 基本生成已确认：整次测试视为已完成探测，速度只是未测出，不把 ok 打成失败，
		// 除非测速之外没有任何已确认能力（不会发生，因为 generate 已确认）。
		result.OK = true
		if result.DurationMS == 0 {
			result.DurationMS = time.Since(start).Milliseconds()
		}
		if result.At == "" {
			result.At = start.UTC().Format(time.RFC3339)
		}
		// 保留已有 generate 输出，不覆盖成测速错误。
		if result.Output == "" && speed.Output != "" {
			result.Output = speed.Output
		}
		if result.TokensPerSecond == 0 {
			result.TokensPerSecond = speed.TokensPerSecond
			result.OutputTokens = speed.OutputTokens
			result.FirstValidResponseMS = speed.FirstValidResponseMS
		}
	}
	report(Progress{Stage: StageSpeed, State: "done", OK: speed.OK})
	result.Capabilities = caps
	if result.At == "" {
		result.At = start.UTC().Format(time.RFC3339)
	}
	if result.DurationMS == 0 {
		result.DurationMS = time.Since(start).Milliseconds()
	}
	return result, m
}

func noteOr(note i18n.Text, fallback i18n.Text) i18n.Text {
	if !note.IsZero() {
		return note
	}
	return fallback
}

func errorFromText(t i18n.Text) error {
	if t.IsZero() {
		return nil
	}
	return errors.New(t.String())
}

func skipRest(report func(Progress), plan []string, after string, info i18n.Text) {
	seen := after == ""
	for _, stage := range plan {
		if !seen {
			if stage == after {
				seen = true
			}
			continue
		}
		report(Progress{Stage: stage, State: "skipped", Info: info})
	}
}

func markUnknownRemaining(caps *config.Capabilities, plan []string, after string, note i18n.Text) {
	seen := after == ""
	for _, stage := range plan {
		if !seen {
			if stage == after {
				seen = true
			}
			continue
		}
		switch stage {
		case StageTools:
			caps.ToolsStatus, caps.ToolsNote = config.CapUnknown, note
		case StageReasoning:
			caps.ReasoningStatus, caps.ReasoningNote = config.CapUnknown, note
		case StageImages:
			caps.ImagesStatus, caps.ImagesNote = config.CapUnknown, note
		case StageCache:
			caps.CacheStatus, caps.CacheNote = config.CapUnknown, note
		case StageSpeed:
			caps.SpeedStatus, caps.SpeedNote = config.CapUnknown, note
		}
	}
}

type probeRun struct {
	m          config.Model
	dial       dialer.Func
	report     func(Progress)
	dropEffort bool
	stage      string
}

func (p *probeRun) chat(ctx context.Context, system string, messages []Message, tools []Tool, onText, onThinking func(string) error) (Message, error) {
	opts := defaultProbeOpts(p.m, len(tools) > 0, p.dropEffort)
	ctx = withProbeOpts(ctx, opts)
	ctx = withHeaderNotify(ctx, func() {
		p.report(Progress{Stage: p.stage, State: "stream", Info: i18n.T("已收到流", "Received the stream")})
	})
	return Chat(ctx, probeRequestModel(p.m), p.dial, system, messages, tools, onText, onThinking)
}

func (p *probeRun) maybeDropEffort(err error) bool {
	if p.dropEffort || probeReasoningEffort(p.m.Model) == "" {
		return false
	}
	var api *APIError
	if errors.As(err, &api) && (api.Kind == KindBadRequest || api.Status == 400) {
		p.dropEffort = true
		slog.Warn("capability_probe 接口拒绝探测用的 reasoning.effort，后续改为不带该参数",
			"model", p.m.Model, "type", p.m.Type, "host", probeEndpointHost(p.m.BaseURL), "error", Explain(err), logx.FileOnly)
		return true
	}
	return false
}

func (p *probeRun) probeGenerate(ctx context.Context, caps *config.Capabilities) (bool, error) {
	p.stage = StageGenerate
	p.report(Progress{Stage: StageGenerate, State: "start", Info: i18n.T("先发一条短文本，确认模型能返回内容", "Sending a short text request to confirm the model replies")})
	p.report(Progress{Stage: StageGenerate, State: "waiting_headers", Info: i18n.T("正等响应头", "Waiting for response headers")})
	sctx, cancel := stageCtx(ctx, probeGenerateBudget)
	defer cancel()
	started := time.Now()
	messages := []Message{{Role: "user", Content: "Reply with exactly OK."}}
	msg, err := p.chat(sctx, "", messages, nil, nil, nil)
	if err != nil && p.maybeDropEffort(err) {
		sctx2, cancel2 := stageCtx(ctx, probeGenerateBudget)
		msg, err = p.chat(sctx2, "", messages, nil, nil, nil)
		cancel2()
	}
	elapsed := time.Since(started)
	if err != nil {
		st, note := classifyProbeErr(err)
		if st == config.CapUnknown && (errors.Is(err, context.DeadlineExceeded) || isTimeout(err)) && p.m.Type == "openai-responses" {
			note = p.contrastChat(ctx, note, elapsed)
		}
		caps.GenerateStatus, caps.GenerateNote = st, note
		caps.Generate = false
		slog.Warn("capability_probe 基本生成未完成",
			"model", p.m.Model, "type", p.m.Type, "host", probeEndpointHost(p.m.BaseURL),
			"elapsed_ms", elapsed.Milliseconds(), "error", Explain(err), logx.FileOnly)
		p.report(Progress{Stage: StageGenerate, State: "done", OK: false, Info: note})
		return false, err
	}
	if strings.TrimSpace(msg.Content) == "" && msg.Reasoning == "" && len(msg.ToolCalls) == 0 {
		note := i18n.T("收到响应但没有文本或思考内容，未测出", "Got a response with no text or reasoning; not determined")
		caps.GenerateStatus, caps.GenerateNote = config.CapUnknown, note
		p.report(Progress{Stage: StageGenerate, State: "done", OK: false, Info: note})
		return false, nil
	}
	caps.Generate, caps.GenerateStatus = true, config.CapConfirmed
	p.report(Progress{Stage: StageGenerate, State: "done", OK: true, Info: i18n.T("已收到模型输出", "Received model output")})
	return true, nil
}

func (p *probeRun) contrastChat(ctx context.Context, base i18n.Text, responsesElapsed time.Duration) i18n.Text {
	sctx, cancel := stageCtx(ctx, probeContrastBudget)
	defer cancel()
	copyM := p.m
	copyM.Type = "openai-chat"
	started := time.Now()
	opts := defaultProbeOpts(copyM, false, p.dropEffort)
	ctx2 := withProbeOpts(sctx, opts)
	_, err := Chat(ctx2, probeRequestModel(copyM), p.dial, "", []Message{{Role: "user", Content: "Reply with exactly OK."}}, nil, nil, nil)
	elapsed := time.Since(started)
	if err == nil {
		return i18n.Join(" ", base, i18n.Tf("已配置的 Responses 在 %d ms 内未返回响应头；对照 Chat Completions 在 %d ms 内返回了内容（未改你配置的类型）。", "The configured Responses request did not return headers in %d ms; a Chat Completions comparison returned content in %d ms (configured type unchanged).", responsesElapsed.Milliseconds(), elapsed.Milliseconds()))
	}
	return i18n.Join(" ", base, i18n.Tf("已配置的 Responses 在 %d ms 内未返回响应头；对照 Chat Completions 同样未在 %d ms 内返回（%s）；未改你配置的类型。", "The configured Responses request did not return headers in %d ms; a Chat Completions comparison also did not return in %d ms (%s); configured type unchanged.", responsesElapsed.Milliseconds(), elapsed.Milliseconds(), Explain(err)))
}

func (p *probeRun) probeTools(ctx context.Context, caps *config.Capabilities) {
	p.stage = StageTools
	p.report(Progress{Stage: StageTools, State: "start", Info: i18n.T("让模型调用一次探测工具", "Asking the model to call a probe tool")})
	p.report(Progress{Stage: StageTools, State: "waiting_headers", Info: i18n.T("正等响应头", "Waiting for response headers")})
	catalog := []Tool{{
		Name:        "ping_probe",
		Description: "Return pong",
		Parameters:  []byte(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`),
	}}
	messages := []Message{{Role: "user", Content: "Call ping_probe with x=hi. Do not answer in plain text."}}
	sctx, cancel := stageCtx(ctx, probeToolsBudget)
	defer cancel()
	started := time.Now()
	onThinking := func(string) error { return nil }
	onText := func(string) error { return nil }
	// 工具参数到达时由 Chat 内部累积；这里在收到流之后把说明换成等待工具参数。
	msg, err := p.chat(sctx, "You are a tool-calling probe.", messages, catalog, onText, onThinking)
	if err != nil && p.maybeDropEffort(err) {
		sctx2, cancel2 := stageCtx(ctx, probeToolsBudget)
		msg, err = p.chat(sctx2, "You are a tool-calling probe.", messages, catalog, onText, onThinking)
		cancel2()
	}
	elapsed := time.Since(started)
	if err != nil {
		st, note := classifyProbeErr(err)
		caps.ToolsStatus, caps.Tools, caps.ToolsNote = st, false, note
		slog.Warn("capability_probe 工具探测请求失败",
			"model", p.m.Model, "type", p.m.Type, "host", probeEndpointHost(p.m.BaseURL),
			"elapsed_ms", elapsed.Milliseconds(), "error", Explain(err), logx.FileOnly)
		p.report(Progress{Stage: StageTools, State: "done", OK: false, Info: note})
		return
	}
	if len(msg.ToolCalls) == 0 {
		note := i18n.T("模型返回了内容但未调用探测工具，未测出", "The model replied but did not call the probe tool; not determined")
		caps.ToolsStatus, caps.Tools, caps.ToolsNote = config.CapUnknown, false, note
		p.report(Progress{Stage: StageTools, State: "done", OK: false, Info: note})
		return
	}
	caps.Tools, caps.ToolsStatus = true, config.CapConfirmed
	p.report(Progress{Stage: StageTools, State: "done", OK: true, Info: i18n.T("已测出工具调用", "Tool call confirmed")})
}

func (p *probeRun) probeReasoning(ctx context.Context, caps *config.Capabilities) bool {
	p.stage = StageReasoning
	p.report(Progress{Stage: StageReasoning, State: "start", Info: i18n.T("发送一条带推理档的短请求", "Sending a short request with a reasoning setting")})
	p.report(Progress{Stage: StageReasoning, State: "waiting_headers", Info: i18n.T("正等响应头", "Waiting for response headers")})
	copyM := p.m
	copyM.Reasoning = true
	if copyM.Effort == "" {
		copyM.Effort = "low"
	}
	prev := p.m
	p.m = copyM
	sctx, cancel := stageCtx(ctx, probeReasoningBudget)
	_, err := p.chat(sctx, "", []Message{{Role: "user", Content: "Reply with exactly OK."}}, nil, nil, nil)
	cancel()
	p.m = prev
	if err != nil && p.maybeDropEffort(err) {
		p.m = copyM
		sctx2, cancel2 := stageCtx(ctx, probeReasoningBudget)
		_, err = p.chat(sctx2, "", []Message{{Role: "user", Content: "Reply with exactly OK."}}, nil, nil, nil)
		cancel2()
		p.m = prev
	}
	if err != nil {
		st, note := classifyProbeErr(err)
		caps.ReasoningStatus, caps.Reasoning, caps.ReasoningNote = st, false, note
		p.report(Progress{Stage: StageReasoning, State: "done", OK: false, Info: note})
		return st == config.CapConfirmed
	}
	caps.Reasoning, caps.ReasoningStatus = true, config.CapConfirmed
	p.report(Progress{Stage: StageReasoning, State: "done", OK: true})
	return true
}

func (p *probeRun) probeImages(ctx context.Context, caps *config.Capabilities) {
	p.stage = StageImages
	p.report(Progress{Stage: StageImages, State: "start", Info: i18n.T("发送一张图片，看模型能否读到", "Sending an image to see if the model can read it")})
	p.report(Progress{Stage: StageImages, State: "waiting_headers", Info: i18n.T("正等响应头", "Waiting for response headers")})
	png, _ := base64.StdEncoding.DecodeString(probeImageBase64)
	messages := []Message{{
		Role:    "user",
		Content: "Describe the image in one word.",
		Images:  []Image{{MIME: "image/png", Data: png}},
	}}
	sctx, cancel := stageCtx(ctx, probeImagesBudget)
	msg, err := p.chat(sctx, "", messages, nil, nil, nil)
	cancel()
	if err != nil {
		st, note := classifyProbeErr(err)
		caps.ImagesStatus, caps.Images, caps.ImagesNote = st, false, note
		p.report(Progress{Stage: StageImages, State: "done", OK: false, Info: note})
		return
	}
	if strings.TrimSpace(msg.Content) == "" && msg.Reasoning == "" {
		note := i18n.T("图片请求有响应但没有可读输出，未测出", "The image request returned no readable output; not determined")
		caps.ImagesStatus, caps.Images, caps.ImagesNote = config.CapUnknown, false, note
		p.report(Progress{Stage: StageImages, State: "done", OK: false, Info: note})
		return
	}
	caps.Images, caps.ImagesStatus = true, config.CapConfirmed
	p.report(Progress{Stage: StageImages, State: "done", OK: true})
}

func (p *probeRun) probeCache(ctx context.Context, caps *config.Capabilities) {
	p.stage = StageCache
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < probeCacheMinRemain {
		note := i18n.T("剩余时间不足，缓存未测", "Not enough time left; cache not tested")
		caps.CacheStatus, caps.CacheNote = config.CapUnknown, note
		p.report(Progress{Stage: StageCache, State: "skipped", Info: note})
		return
	}
	p.report(Progress{Stage: StageCache, State: "start", Info: i18n.T("用同一前缀发两次，看是否命中缓存", "Sending the same prefix twice to check for a cache hit")})
	sys := strings.Repeat("Cache prefix probe. ", 500)
	messages := []Message{{Role: "user", Content: "Reply with exactly OK."}}
	sctx, cancel := stageCtx(ctx, probeCacheBudget)
	defer cancel()
	if _, err := p.chat(sctx, sys, messages, nil, nil, nil); err != nil {
		st, note := classifyProbeErr(err)
		caps.CacheStatus, caps.CacheNote = st, note
		p.report(Progress{Stage: StageCache, State: "done", OK: false, Info: note})
		return
	}
	second, err := p.chat(sctx, sys, messages, nil, nil, nil)
	if err != nil {
		st, note := classifyProbeErr(err)
		caps.CacheStatus, caps.CacheNote = st, note
		p.report(Progress{Stage: StageCache, State: "done", OK: false, Info: note})
		return
	}
	if second.CacheTokens > 0 {
		caps.CacheHit, caps.CacheStatus = true, config.CapConfirmed
		p.report(Progress{Stage: StageCache, State: "done", OK: true})
		return
	}
	note := i18n.T("两次请求均完成但未见缓存命中", "Both requests finished but no cache hit was reported")
	caps.CacheStatus, caps.CacheHit, caps.CacheNote = config.CapUnknown, false, note
	p.report(Progress{Stage: StageCache, State: "done", OK: false, Info: note})
}

// probeRequestModel 只改探测请求用的副本，不改写回配置的窗口/输出上限。
func probeRequestModel(m config.Model) config.Model {
	if m.MaxOutputTokens <= 0 || m.MaxOutputTokens > probeMaxOutputTokens {
		m.MaxOutputTokens = probeMaxOutputTokens
	}
	return m
}

func probeEndpointHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// 保留给单测：用探测选项走工具阶段。
func probeTools(ctx context.Context, m config.Model, dial dialer.Func) (bool, error) {
	caps := &config.Capabilities{}
	run := &probeRun{m: m, dial: dial, report: func(Progress) {}}
	run.probeTools(ctx, caps)
	if caps.Tools {
		return true, nil
	}
	if !caps.ToolsNote.IsZero() {
		return false, errors.New(caps.ToolsNote.String())
	}
	return false, nil
}

// probeImageBase64 是 256x256 的纯色 PNG。1x1 这类退化图会被部分端点（DeepSeek）判成
// 不支持的图片，探测就会误报不支持看图。
const probeImageBase64 = "iVBORw0KGgoAAAANSUhEUgAAAQAAAAEACAYAAABccqhmAAAAAXNSR0IArs4c6QAAAARnQU1BAACxjwv8YQUAAAAJcEhZcwAADsMAAA7DAcdvqGQAAAQMSURBVHhe7dQxEYBAAAPBF4slZKIBegxckb2ZbSIg55U02/kPknZyANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwDkAazgFIwzkAaTgHIA3nAKThHIA0nAOQhnMA0nAOQBrOAUjDOQBpOAcgDecApOEcgDScA5CGcwDScA5AGs4BSMM5AGk4ByAN5wCk4RyANJwDkIZzANJwH4f9QpKddqKHAAAAAElFTkSuQmCC"

// ProbeTotalBudget 是设置页一次测试的有限总时限，供 App 层套在请求 ctx 上。
func ProbeTotalBudget() time.Duration { return probeTotalBudget }
