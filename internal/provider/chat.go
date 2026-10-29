package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/runlog"
)

// Message 是与接口无关的对话消息。Role 取 system、user、assistant、tool。
// Reasoning 是助手的思考内容；ReasoningSignature 是 Anthropic thinking 块的签名。
// Truncated 表示这次输出因长度被截断，调用方应让模型分步继续。
type Message struct {
	Role               string     `json:"role"`
	Content            string     `json:"content,omitempty"`
	ToolCalls          []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID         string     `json:"tool_call_id,omitempty"`
	IsError            bool       `json:"is_error,omitempty"`
	Reasoning          string     `json:"reasoning,omitempty"`
	ReasoningSignature string     `json:"reasoning_signature,omitempty"`
	// ReasoningBy 记下产生这段推理（及其签名）的端点+模型，见 ReasoningIssuer。
	ReasoningBy string  `json:"reasoning_by,omitempty"`
	Truncated   bool    `json:"truncated,omitempty"`
	Images      []Image `json:"images,omitempty"`
	// Card 是这条工具结果在 Cursor 里的卡片。不发给模型接口。
	Card *cursorpb.ToolCall `json:"-"`
	// Shown 是用户原文。Content 里可能包了时间戳和 user_query，那只给模型看，不能写进对话记录。
	Shown            string `json:"-"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	CacheTokens      int    `json:"cache_tokens,omitempty"`
}

// Image 是用户消息里附带的图片。
type Image struct {
	MIME string `json:"mime,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// ToolCall 的 Arguments 是模型给出的 JSON 文本。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatRequest struct {
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Chat 流式调用模型。文本增量交给 onText，思考增量交给 onThinking（可以为 nil）。
// 还没有向调用方输出任何文本时，限流、5xx、传输失败、空响应和流卡死会重试。
func Chat(ctx context.Context, m config.Model, dial dialer.Func, system string, messages []Message, tools []Tool, onText func(string) error, onThinking func(string) error) (Message, error) {
	messages = StripForeignReasoning(m, messages)
	msg, err := chatRetry(ctx, m, dial, system, messages, tools, onText, onThinking)
	if err == nil && (msg.Reasoning != "" || msg.ReasoningSignature != "") {
		msg.ReasoningBy = ReasoningIssuer(m)
	}
	return msg, err
}

func chatRetry(ctx context.Context, m config.Model, dial dialer.Func, system string, messages []Message, tools []Tool, onText func(string) error, onThinking func(string) error) (Message, error) {
	if len(messages) == 0 {
		return Message{}, i18n.E("没有用户消息", "No user message")
	}
	tools = uniqueTools(tools)
	// 这个端点+模型已经拒绝过这一档 reasoning_effort，就直接不发，省掉每轮一次 400。
	if m.Effort != "" {
		if rejected, ok := effortMem.Load(effortKey(m)); ok && rejected.(bool) {
			m.Effort = ""
		}
	}
	var last error
	started := time.Now()
	useText := TextToolActive(m) // 端点不支持原生工具时，把工具规格写进系统提示，由救援解析读回
	immediate := false
	timeouts, empties := 0, 0
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && !immediate {
			noteModel(ctx, runlog.Note{Kind: "model_retry", Attempt: attempt, Error: Explain(last), ElapsedMs: time.Since(started).Milliseconds()})
			if err := waitRetry(ctx, last, attempt); err != nil {
				return Message{}, err
			}
		}
		immediate = false
		effSystem, effTools := system, tools
		if useText {
			effSystem, effTools = textToolSystem(system, tools), nil
		}
		noteModel(ctx, runlog.Note{Kind: "model_attempt", Attempt: attempt, Tools: len(effTools), Messages: len(messages)})
		var lastPiece time.Time
		piece := func(kind string, text string, fn func(string) error) error {
			if fn == nil {
				return nil
			}
			gap := int64(0)
			now := time.Now()
			if !lastPiece.IsZero() {
				gap = now.Sub(lastPiece).Milliseconds()
			}
			lastPiece = now
			noteModel(ctx, runlog.Note{Kind: kind, Attempt: attempt, Bytes: len(text), GapMs: gap})
			return fn(text)
		}
		emitted := false
		msg, err := chatOnce(ctx, m, dial, effSystem, messages, effTools, func(text string) error {
			emitted = true
			return piece("model_text", text, onText)
		}, func(text string) error {
			emitted = true
			if onThinking == nil {
				return nil
			}
			return piece("model_thinking", text, onThinking)
		})
		if err == nil {
			msg = applyRescue(msg, tools)
			noteModel(ctx, runlog.Note{
				Kind: "model_done", Attempt: attempt, ElapsedMs: time.Since(started).Milliseconds(),
				Tools: len(msg.ToolCalls), Prompt: msg.PromptTokens, Output: msg.CompletionTokens,
				Bytes: len(msg.Content) + len(msg.Reasoning),
			})
			return msg, nil
		}
		last = err
		noteModel(ctx, modelError(attempt, time.Since(started), err))
		// 端点拒绝 Cursor 传来的 reasoning_effort 档位（闭集里没有这一档）时，去掉字段重试一次，
		// 由端点用自己的默认档。只认错误里明确点名 reasoning_effort 的 400。
		if !emitted && m.Effort != "" && wantsEffortFallback(err) {
			effortMem.Store(effortKey(m), true)
			m.Effort = ""
			noteModel(ctx, runlog.Note{Kind: "model_reasoning_effort_dropped", Attempt: attempt, Error: Explain(err)})
			immediate = true
			continue
		}
		// vLLM 没配工具解析器时，带 tools 会被 400 拒绝（报 enable-auto-tool-choice / tool-call-parser）。
		// 记下该端点+模型，改走文本工具协议，并立刻重试一次（不计退避）。
		if !useText && len(tools) > 0 && !emitted && wantsTextToolMode(err) {
			rememberTextTool(m)
			noteModel(ctx, runlog.Note{Kind: "model_text_tool_mode", Attempt: attempt, Error: Explain(err)})
			useText = true
			immediate = true
			continue
		}
		if emitted || !Retryable(err) {
			return Message{}, withRetries(err, attempt)
		}
		if EmptyLarge(err) {
			return Message{}, withRetries(err, attempt)
		}
		if IsEmpty(err) {
			empties++
			slog.Warn("upstream_empty 上游正常结束但没有内容和工具调用", "model", m.Model, "attempt", attempt+1, "elapsed_ms", time.Since(started).Milliseconds())
			if empties >= maxEmptyAttempts {
				return Message{}, withRetries(err, attempt)
			}
		}
		if isTimeout(err) {
			timeouts++
			if timeouts >= maxTimeoutAttempts {
				slog.Warn("upstream_timeout 上游空闲超时次数到上限，向客户端报错", "model", m.Model, "attempts", attempt+1, "error", Explain(err))
				return Message{}, withRetries(err, attempt)
			}
		}
	}
	return Message{}, withRetries(last, maxAttempts-1)
}

// textToolMem 记住哪些端点+模型不支持原生工具（本进程内）。键为 baseURL|model。
var textToolMem sync.Map

// effortMem 记住哪些端点+模型拒绝过哪一档 reasoning_effort（本进程内）。键为 baseURL|model|effort。
var effortMem sync.Map

func effortKey(m config.Model) string {
	return textToolKey(m) + "|" + strings.ToLower(strings.TrimSpace(m.Effort))
}

// uniqueTools 按名字去重，保留第一次出现的定义。有的端点遇到重名工具直接 400
// （DeepSeek 官方：Tool names must be unique）。工具表本身已经保证唯一，这里再兜一层。
func uniqueTools(tools []Tool) []Tool {
	if len(tools) < 2 {
		return tools
	}
	seen := make(map[string]bool, len(tools))
	out := make([]Tool, 0, len(tools))
	var dropped []string
	for _, tool := range tools {
		key := strings.ToLower(strings.TrimSpace(tool.Name))
		if key == "" || seen[key] {
			dropped = append(dropped, tool.Name)
			continue
		}
		seen[key] = true
		out = append(out, tool)
	}
	if len(dropped) > 0 {
		slog.Warn("tools_duplicate 工具重名，已丢弃重复定义", "dropped", strings.Join(dropped, ","), "kept", len(out))
	}
	return out
}

func textToolKey(m config.Model) string {
	return strings.ToLower(strings.TrimSpace(m.BaseURL)) + "|" + strings.ToLower(strings.TrimSpace(m.Model))
}

func rememberTextTool(m config.Model) { textToolMem.Store(textToolKey(m), true) }

// TextToolActive 判断该模型当前是否走文本工具协议：配置里标了，或本进程已探到端点不支持原生工具。
func TextToolActive(m config.Model) bool {
	if m.TextToolMode {
		return true
	}
	v, ok := textToolMem.Load(textToolKey(m))
	return ok && v.(bool)
}

// wantsTextToolMode 判断这是不是“端点不支持原生工具”的 400（vLLM 没配 tool 解析器）。
func wantsTextToolMode(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	if api.Status != 400 && api.Kind != KindBadRequest {
		return false
	}
	s := strings.ToLower(api.Text + " " + api.Detail)
	return strings.Contains(s, "enable-auto-tool-choice") ||
		strings.Contains(s, "tool-call-parser") ||
		strings.Contains(s, "tool call parser")
}

// textToolSystem 把工具规格写进系统提示，格式用现有救援解析认得的 <tool_call>{json}</tool_call>。
func textToolSystem(system string, tools []Tool) string {
	if len(tools) == 0 {
		return system
	}
	var b strings.Builder
	if strings.TrimSpace(system) != "" {
		b.WriteString(system)
		b.WriteString("\n\n")
	}
	b.WriteString("# Calling tools\n")
	b.WriteString("This endpoint does NOT accept native/function tool calls. ")
	b.WriteString("To call a tool, output a block exactly in this form and nothing else around it:\n")
	b.WriteString("<tool_call>\n{\"name\": \"<tool_name>\", \"arguments\": {<json arguments>}}\n</tool_call>\n")
	b.WriteString("Emit one <tool_call> block per call. Do not wrap it in Markdown code fences. ")
	b.WriteString("When the task is done and you need no tool, reply in plain text with no <tool_call> block.\n\n")
	b.WriteString("Available tools:\n")
	for _, t := range tools {
		b.WriteString("\n- ")
		b.WriteString(t.Name)
		if strings.TrimSpace(t.Description) != "" {
			b.WriteString(": ")
			b.WriteString(t.Description)
		}
		if len(t.Parameters) > 0 {
			b.WriteString("\n  JSON Schema: ")
			b.Write(t.Parameters)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func noteModel(ctx context.Context, n runlog.Note) {
	n.Request = runlog.RequestID(ctx)
	runlog.Emit(n)
}

func modelError(attempt int, elapsed time.Duration, err error) runlog.Note {
	n := runlog.Note{Kind: "model_error", Attempt: attempt, ElapsedMs: elapsed.Milliseconds(), Error: Explain(err)}
	var api *APIError
	if errors.As(err, &api) {
		n.Status = api.Status
	}
	return n
}

// ErrStopStream 由 onText/onThinking 返回，表示检测到复读等情况，希望立刻收尾本次生成；
// chatOnce 捕获它，把已累积的内容按正常完成返回，不当作错误上抛。
var ErrStopStream = errors.New("stop streaming")

func chatOnce(ctx context.Context, m config.Model, dial dialer.Func, system string, messages []Message, tools []Tool, onText func(string) error, onThinking func(string) error) (Message, error) {
	maxTokens := m.MaxOutputTokens
	if maxTokens == 0 && (m.Type == "anthropic" || m.Type == "openai-responses") {
		maxTokens = 8192
	}
	var reqBytes int
	resp, err := doSized(ctx, m, dial, chatRequest{System: system, Messages: messages, Tools: tools, MaxTokens: maxTokens}, &reqBytes)
	if err != nil {
		return Message{}, transportError(err)
	}
	defer resp.Body.Close()
	idle, dataIdle := streamIdle, streamDataIdle
	if DetectFamily(m) == FamilyDeepSeek && idle < deepseekStreamIdle {
		// DeepSeek V4 的思考（effort=max、1M 上下文）可能几分钟没有新数据，参考实现给到 5 分钟。
		idle, dataIdle = deepseekStreamIdle, deepseekStreamIdle
	}
	raw := &headBuffer{max: emptyRawHead}
	reader := bufio.NewReaderSize(io.TeeReader(&idleReader{r: resp.Body, idle: idle}, raw), 64*1024)
	empty := func(finish string, msg Message) error {
		return emptyReply(m, messages, reqBytes, finish, msg, raw.Bytes())
	}
	head, _ := reader.Peek(1)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(reader, 4096))
		return Message{}, statusError(resp.StatusCode, body, resp.Header.Get("Retry-After"))
	}
	if len(head) == 1 && head[0] == '{' {
		body, err := io.ReadAll(io.LimitReader(reader, 8<<20))
		if err != nil {
			return Message{}, err
		}
		msg, err := staticMessage(body, m.Type)
		if err != nil {
			return Message{}, err
		}
		if msg.Content == "" && len(msg.ToolCalls) == 0 && !msg.Truncated && msg.Reasoning == "" {
			return Message{}, empty(staticFinish(body), msg)
		}
		if msg.Content != "" {
			if err := onText(msg.Content); err != nil {
				if errors.Is(err, ErrStopStream) {
					return msg, nil
				}
				return Message{}, err
			}
		}
		return msg, nil
	}
	acc := newAccumulator(m.Type, m.Type != "anthropic" && ProfileOf(m).ThinkTagSplit)
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	lastData := time.Now()
	skipped := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			skipped++
			if waited := time.Since(lastData); waited >= dataIdle {
				slog.Warn("upstream_data_idle 上游只发 keepalive、长时间没有数据帧，判定超时", "model", m.Model, "waited_s", int(waited.Seconds()), "non_data_lines", skipped)
				return Message{}, dataIdleError()
			}
			continue
		}
		lastData = time.Now()
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		piece, err := acc.feed([]byte(data))
		if err != nil {
			return Message{}, err
		}
		if piece.thinking != "" && onThinking != nil {
			if err := onThinking(piece.thinking); err != nil {
				if errors.Is(err, ErrStopStream) {
					return acc.message(), nil
				}
				return Message{}, err
			}
		}
		if piece.text != "" {
			if err := onText(piece.text); err != nil {
				if errors.Is(err, ErrStopStream) {
					return acc.message(), nil
				}
				return Message{}, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		var api *APIError
		if errors.As(err, &api) {
			return Message{}, err
		}
		return Message{}, transportError(err)
	}
	msg := acc.message()
	if msg.Content == "" && len(msg.ToolCalls) == 0 && !msg.Truncated && msg.Reasoning == "" {
		return Message{}, empty(acc.finish, msg)
	}
	return msg, nil
}

func do(ctx context.Context, m config.Model, dial dialer.Func, req chatRequest) (*http.Response, error) {
	return doSized(ctx, m, dial, req, nil)
}

// doSized 同 do，另把请求体字节数写进 size（可为 nil），供空回复诊断使用。
func doSized(ctx context.Context, m config.Model, dial dialer.Func, req chatRequest, size *int) (*http.Response, error) {
	endpoint, err := RequestURL(m)
	if err != nil {
		return nil, err
	}
	payload, err := requestBody(m, req)
	if err != nil {
		return nil, err
	}
	if size != nil {
		*size = len(payload)
	}
	noteModel(ctx, runlog.Note{Kind: "model_request", Bytes: len(payload)})
	asked := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if m.Type == "anthropic" {
		httpReq.Header.Set("x-api-key", m.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext:           dial,
		Proxy:                 nil,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		ResponseHeaderTimeout: responseHeaderTimeout,
	}}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	noteModel(ctx, runlog.Note{Kind: "model_status", Status: resp.StatusCode, ElapsedMs: time.Since(asked).Milliseconds()})
	return resp, nil
}

func requestBody(m config.Model, req chatRequest) ([]byte, error) {
	switch m.Type {
	case "openai-chat":
		return openAIBody(m, req)
	case "openai-responses":
		return responsesBody(m, req)
	case "anthropic":
		return anthropicBody(m, req)
	default:
		return nil, i18n.E("接口类型只支持 openai-chat、openai-responses 和 anthropic", "Endpoint type must be openai-chat, openai-responses or anthropic")
	}
}

func anthropicEffort(effort string) string {
	switch effort {
	case "xhigh", "max":
		return "high"
	default:
		return effort
	}
}

func openAIBody(m config.Model, req chatRequest) ([]byte, error) {
	type function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Arguments   *string         `json:"arguments,omitempty"`
	}
	type call struct {
		ID       string   `json:"id"`
		Type     string   `json:"type"`
		Function function `json:"function"`
	}
	type msg struct {
		Role             string  `json:"role"`
		Content          any     `json:"content"`
		ToolCalls        []call  `json:"tool_calls,omitempty"`
		ToolCallID       string  `json:"tool_call_id,omitempty"`
		ReasoningContent *string `json:"reasoning_content,omitempty"`
	}
	type tool struct {
		Type     string   `json:"type"`
		Function function `json:"function"`
	}
	body := struct {
		Model              string         `json:"model"`
		Messages           []msg          `json:"messages"`
		Tools              []tool         `json:"tools,omitempty"`
		Stream             bool           `json:"stream"`
		MaxTokens          int            `json:"max_tokens,omitempty"`
		ReasoningEffort    string         `json:"reasoning_effort,omitempty"`
		ServiceTier        string         `json:"service_tier,omitempty"`
		PromptCacheKey     string         `json:"prompt_cache_key,omitempty"`
		EnableThinking     *bool          `json:"enable_thinking,omitempty"`
		ThinkingBudget     *int           `json:"thinking_budget,omitempty"`
		ParallelToolCalls  *bool          `json:"parallel_tool_calls,omitempty"`
		ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
		StreamOptions      struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}{Model: m.Model, Stream: true, MaxTokens: req.MaxTokens, ReasoningEffort: m.Effort, PromptCacheKey: strings.TrimSpace(m.PromptCacheKey)}
	if m.Fast {
		body.ServiceTier = "fast"
		slog.Debug("service_tier", "value", "fast")
	}
	body.StreamOptions.IncludeUsage = true
	if et := qwenEnableThinking(m); et != nil {
		body.EnableThinking = et
		if !*et {
			// vLLM 只认 chat_template_kwargs 里的 enable_thinking，顶层字段会被忽略，
			// 所以关思考时两处都发（qwen3-32b 等在 vLLM 上才真关得掉思考）。
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
		}
	}
	body.ThinkingBudget = qwenThinkingBudget(m)
	if len(req.Tools) > 0 && ProfileOf(m).ParallelToolCalls {
		yes := true
		body.ParallelToolCalls = &yes
	}
	if req.System != "" {
		body.Messages = append(body.Messages, msg{Role: "system", Content: req.System})
	}
	for _, message := range req.Messages {
		out := msg{Role: message.Role, Content: openAIContent(message), ToolCallID: message.ToolCallID, ReasoningContent: reasoningField(m, message)}
		for _, c := range message.ToolCalls {
			args := c.Arguments
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, call{ID: c.ID, Type: "function", Function: function{Name: c.Name, Arguments: &args}})
		}
		if message.Role == "assistant" && message.Content == "" && len(message.Images) == 0 && len(out.ToolCalls) > 0 {
			out.Content = nil
		}
		body.Messages = append(body.Messages, out)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, tool{Type: "function", Function: function{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
	}
	return json.Marshal(body)
}

func anthropicBody(m config.Model, req chatRequest) ([]byte, error) {
	type imageSource struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type cacheControl struct {
		Type string `json:"type"`
	}
	type block struct {
		Type         string          `json:"type"`
		Text         string          `json:"text,omitempty"`
		Thinking     string          `json:"thinking,omitempty"`
		Signature    string          `json:"signature,omitempty"`
		ID           string          `json:"id,omitempty"`
		Name         string          `json:"name,omitempty"`
		Input        json.RawMessage `json:"input,omitempty"`
		ToolUseID    string          `json:"tool_use_id,omitempty"`
		Content      any             `json:"content,omitempty"`
		IsError      bool            `json:"is_error,omitempty"`
		Source       *imageSource    `json:"source,omitempty"`
		CacheControl *cacheControl   `json:"cache_control,omitempty"`
	}
	type msg struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	body := struct {
		Model        string  `json:"model"`
		System       []block `json:"system,omitempty"`
		Messages     []msg   `json:"messages"`
		Tools        []tool  `json:"tools,omitempty"`
		Stream       bool    `json:"stream"`
		MaxTokens    int     `json:"max_tokens"`
		OutputConfig any     `json:"output_config,omitempty"`
	}{Model: m.Model, Stream: true, MaxTokens: req.MaxTokens}
	if req.System != "" {
		body.System = []block{{Type: "text", Text: req.System, CacheControl: &cacheControl{Type: "ephemeral"}}}
	}
	if effort := anthropicEffort(m.Effort); effort != "" {
		body.OutputConfig = map[string]string{"effort": effort}
	}
	push := func(role string, b block) {
		if n := len(body.Messages); n > 0 && body.Messages[n-1].Role == role {
			body.Messages[n-1].Content = append(body.Messages[n-1].Content, b)
			return
		}
		body.Messages = append(body.Messages, msg{Role: role, Content: []block{b}})
	}
	for _, message := range req.Messages {
		switch message.Role {
		case "assistant":
			if message.Reasoning != "" && message.ReasoningSignature != "" {
				push("assistant", block{Type: "thinking", Thinking: message.Reasoning, Signature: message.ReasoningSignature})
			}
			if message.Content != "" {
				push("assistant", block{Type: "text", Text: message.Content})
			}
			for _, c := range message.ToolCalls {
				input := json.RawMessage(c.Arguments)
				if !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				push("assistant", block{Type: "tool_use", ID: c.ID, Name: c.Name, Input: input})
			}
		case "tool":
			push("user", block{Type: "tool_result", ToolUseID: message.ToolCallID, Content: anthropicToolContent(message), IsError: message.IsError})
		default:
			push("user", block{Type: "text", Text: message.Content})
			for _, image := range message.Images {
				push("user", block{Type: "image", Source: &imageSource{Type: "base64", MediaType: image.MIME, Data: base64.StdEncoding.EncodeToString(image.Data)}})
			}
		}
	}
	for i := len(body.Messages) - 1; i >= 0; i-- {
		if body.Messages[i].Role != "user" || len(body.Messages[i].Content) == 0 {
			continue
		}
		last := len(body.Messages[i].Content) - 1
		body.Messages[i].Content[last].CacheControl = &cacheControl{Type: "ephemeral"}
		break
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, tool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	return json.Marshal(body)
}

// accumulator 把流式事件拼成完整的助手消息。
type piece struct {
	text     string
	thinking string
}

type accumulator struct {
	kind             string
	text             strings.Builder
	reasoning        strings.Builder
	signature        strings.Builder
	finish           string
	promptTokens     int
	completionTokens int
	cacheTokens      int
	calls            map[int]*ToolCall
	args             map[int]*strings.Builder
	seenID           map[int]string  // 每个槽位已见过的调用 id，用于识别 Ollama 不带 index 的多调用
	itemSlot         map[string]int  // Responses 输出项 id 到槽位
	streamedText     map[string]bool // Responses 已经按增量收到正文的输出项
	maxIndex         int
	thinker          *inlineThink // 非空时剥离 content 里的 <think> 推理标签
}

func newAccumulator(kind string, splitThink bool) *accumulator {
	a := &accumulator{kind: kind, calls: map[int]*ToolCall{}, args: map[int]*strings.Builder{}, seenID: map[int]string{}, itemSlot: map[string]int{}, streamedText: map[string]bool{}, maxIndex: -1}
	if splitThink {
		a.thinker = &inlineThink{}
	}
	return a
}

func (a *accumulator) call(index int) (*ToolCall, *strings.Builder) {
	if a.calls[index] == nil {
		a.calls[index] = &ToolCall{}
		a.args[index] = &strings.Builder{}
	}
	return a.calls[index], a.args[index]
}

func (a *accumulator) feed(data []byte) (piece, error) {
	if a.kind == "openai-responses" {
		return a.feedResponses(data)
	}
	if a.kind == "anthropic" {
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage anthropicUsage `json:"usage"`
		}
		if json.Unmarshal(data, &event) != nil {
			return piece{}, nil
		}
		switch event.Type {
		case "message_start":
			a.addAnthropicUsage(event.Message.Usage)
		case "error":
			return piece{}, i18n.Ef("接口返回错误：%s", "Endpoint error: %s", event.Error.Message)
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				c, _ := a.call(event.Index)
				c.ID, c.Name = event.ContentBlock.ID, event.ContentBlock.Name
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				a.text.WriteString(event.Delta.Text)
				return piece{text: event.Delta.Text}, nil
			case "thinking_delta":
				a.reasoning.WriteString(event.Delta.Thinking)
				return piece{thinking: event.Delta.Thinking}, nil
			case "signature_delta":
				a.signature.WriteString(event.Delta.Signature)
			case "input_json_delta":
				_, args := a.call(event.Index)
				args.WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			a.addAnthropicUsage(event.Usage)
			if event.Delta.StopReason != "" {
				a.finish = event.Delta.StopReason
			}
		}
		return piece{}, nil
	}
	var event struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			PromptTokens         int `json:"prompt_tokens"`
			CompletionTokens     int `json:"completion_tokens"`
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
			PromptTokensDetails  struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &event) != nil {
		return piece{}, nil
	}
	if event.Usage.PromptTokens > 0 {
		a.promptTokens = event.Usage.PromptTokens
	}
	if event.Usage.CompletionTokens > 0 {
		a.completionTokens = event.Usage.CompletionTokens
	}
	cached := event.Usage.PromptCacheHitTokens
	if event.Usage.PromptTokensDetails.CachedTokens > cached {
		cached = event.Usage.PromptTokensDetails.CachedTokens
	}
	if cached > 0 {
		a.cacheTokens = cached
	}
	if event.Error != nil && event.Error.Message != "" {
		return piece{}, i18n.Ef("接口返回错误：%s", "Endpoint error: %s", event.Error.Message)
	}
	if len(event.Choices) == 0 {
		return piece{}, nil
	}
	choice := event.Choices[0]
	if choice.FinishReason != "" {
		a.finish = choice.FinishReason
	}
	delta := choice.Delta
	for _, tc := range delta.ToolCalls {
		// Ollama 的 OpenAI 兼容流对多个工具调用都用 index 0，但带不同的 id；
		// 看到同一槽位换了新 id 就新开一个槽位，避免把两次调用的参数拼在一起。
		idx := tc.Index
		if tc.ID != "" {
			if prev, ok := a.seenID[idx]; ok && prev != tc.ID {
				a.maxIndex++
				idx = a.maxIndex
			}
			a.seenID[idx] = tc.ID
		}
		if idx > a.maxIndex {
			a.maxIndex = idx
		}
		c, args := a.call(idx)
		if tc.ID != "" {
			c.ID = tc.ID
		}
		if tc.Function.Name != "" {
			c.Name += tc.Function.Name
		}
		args.WriteString(tc.Function.Arguments)
	}
	think := delta.ReasoningContent
	if think == "" {
		think = delta.Reasoning
	}
	content := delta.Content
	if a.thinker != nil && content != "" {
		tx, th := a.thinker.feed(content)
		content = tx
		think += th
	}
	a.reasoning.WriteString(think)
	a.text.WriteString(content)
	return piece{text: content, thinking: think}, nil
}

func (a *accumulator) message() Message {
	if a.thinker != nil {
		tx, th := a.thinker.flush()
		a.text.WriteString(tx)
		a.reasoning.WriteString(th)
	}
	msg := Message{Role: "assistant", Content: a.text.String(), Reasoning: a.reasoning.String(), ReasoningSignature: a.signature.String(), PromptTokens: a.promptTokens, CompletionTokens: a.completionTokens, CacheTokens: a.cacheTokens}
	indexes := make([]int, 0, len(a.calls))
	for i := range a.calls {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		c := *a.calls[i]
		c.Arguments = a.args[i].String()
		if c.Name == "" {
			continue
		}
		msg.ToolCalls = append(msg.ToolCalls, c)
	}
	if truncated(a.finish, msg.ToolCalls) {
		msg.Truncated = true
		msg.ToolCalls = dropIncompleteTail(msg.ToolCalls)
	}
	return msg
}

func truncated(finish string, calls []ToolCall) bool {
	switch finish {
	case "length", "max_tokens":
		return true
	}
	if finish == "" && len(calls) > 0 && !json.Valid([]byte(calls[len(calls)-1].Arguments)) {
		return true
	}
	return false
}

func dropIncompleteTail(calls []ToolCall) []ToolCall {
	for len(calls) > 0 && !json.Valid([]byte(calls[len(calls)-1].Arguments)) {
		calls = calls[:len(calls)-1]
	}
	return calls
}

func staticMessage(raw []byte, kind string) (Message, error) {
	msg := Message{Role: "assistant"}
	if kind == "anthropic" {
		var body struct {
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Thinking  string          `json:"thinking"`
				Signature string          `json:"signature"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
			} `json:"content"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return msg, err
		}
		if body.Error.Message != "" {
			return msg, errors.New(body.Error.Message)
		}
		for _, part := range body.Content {
			switch part.Type {
			case "text":
				msg.Content += part.Text
			case "thinking":
				msg.Reasoning += part.Thinking
				if part.Signature != "" {
					msg.ReasoningSignature = part.Signature
				}
			case "tool_use":
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: part.ID, Name: part.Name, Arguments: string(part.Input)})
			}
		}
		return msg, nil
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return msg, err
	}
	if body.Error.Message != "" {
		return msg, errors.New(body.Error.Message)
	}
	if len(body.Choices) > 0 {
		msg.Content = body.Choices[0].Message.Content
		msg.Reasoning = body.Choices[0].Message.ReasoningContent
		for _, tc := range body.Choices[0].Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
	}
	return msg, nil
}

func anthropicToolContent(message Message) any {
	if len(message.Images) == 0 {
		return message.Content
	}
	parts := []any{map[string]string{"type": "text", "text": message.Content}}
	for _, image := range message.Images {
		parts = append(parts, map[string]any{
			"type": "image",
			"source": map[string]string{
				"type":       "base64",
				"media_type": image.MIME,
				"data":       base64.StdEncoding.EncodeToString(image.Data),
			},
		})
	}
	return parts
}

func openAIContent(message Message) any {
	if len(message.Images) == 0 {
		return message.Content
	}
	parts := []any{map[string]string{"type": "text", "text": message.Content}}
	for _, image := range message.Images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": "data:" + image.MIME + ";base64," + base64.StdEncoding.EncodeToString(image.Data)},
		})
	}
	return parts
}

// anthropicUsage 是 Messages 流里 message_start / message_delta 带的用量。
// input_tokens 不含缓存部分，提示总量要把读缓存和写缓存加回来，命中率才和 OpenAI 口径一致。
type anthropicUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

func (a *accumulator) addAnthropicUsage(u anthropicUsage) {
	if p := u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens; p > 0 {
		a.promptTokens = p
	}
	if u.OutputTokens > 0 {
		a.completionTokens = u.OutputTokens
	}
	if u.CacheReadTokens > 0 {
		a.cacheTokens = u.CacheReadTokens
	}
}
