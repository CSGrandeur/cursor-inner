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

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
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
	Truncated          bool       `json:"truncated,omitempty"`
	Images             []Image    `json:"images,omitempty"`
	// Card 是这条工具结果在 Cursor 里的卡片。不发给模型接口。
	Card             *cursorpb.ToolCall `json:"-"`
	PromptTokens     int                `json:"prompt_tokens,omitempty"`
	CompletionTokens int                `json:"completion_tokens,omitempty"`
	CacheTokens      int                `json:"cache_tokens,omitempty"`
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
	if len(messages) == 0 {
		return Message{}, i18n.E("没有用户消息", "No user message")
	}
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := waitRetry(ctx, last, attempt); err != nil {
				return Message{}, err
			}
		}
		emitted := false
		msg, err := chatOnce(ctx, m, dial, system, messages, tools, func(text string) error {
			emitted = true
			return onText(text)
		}, func(text string) error {
			emitted = true
			if onThinking == nil {
				return nil
			}
			return onThinking(text)
		})
		if err == nil {
			return msg, nil
		}
		last = err
		if emitted || !Retryable(err) {
			return Message{}, withRetries(err, attempt)
		}
	}
	return Message{}, withRetries(last, maxAttempts-1)
}

func chatOnce(ctx context.Context, m config.Model, dial dialer.Func, system string, messages []Message, tools []Tool, onText func(string) error, onThinking func(string) error) (Message, error) {
	maxTokens := m.MaxOutputTokens
	if maxTokens == 0 && m.Type == "anthropic" {
		maxTokens = 8192
	}
	resp, err := do(ctx, m, dial, chatRequest{System: system, Messages: messages, Tools: tools, MaxTokens: maxTokens})
	if err != nil {
		return Message{}, transportError(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReaderSize(&idleReader{r: resp.Body, idle: streamIdle}, 64*1024)
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
		if msg.Content != "" {
			if err := onText(msg.Content); err != nil {
				return Message{}, err
			}
		}
		return msg, nil
	}
	acc := newAccumulator(m.Type)
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
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
				return Message{}, err
			}
		}
		if piece.text != "" {
			if err := onText(piece.text); err != nil {
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
		return Message{}, emptyError()
	}
	return msg, nil
}

func do(ctx context.Context, m config.Model, dial dialer.Func, req chatRequest) (*http.Response, error) {
	endpoint, err := RequestURL(m)
	if err != nil {
		return nil, err
	}
	payload, err := requestBody(m, req)
	if err != nil {
		return nil, err
	}
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
		DialContext:       dial,
		Proxy:             nil,
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}}
	return client.Do(httpReq)
}

func requestBody(m config.Model, req chatRequest) ([]byte, error) {
	switch m.Type {
	case "openai-chat":
		return openAIBody(m, req)
	case "anthropic":
		return anthropicBody(m, req)
	default:
		return nil, i18n.E("接口类型只支持 openai-chat 和 anthropic", "Endpoint type must be openai-chat or anthropic")
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
		Model           string `json:"model"`
		Messages        []msg  `json:"messages"`
		Tools           []tool `json:"tools,omitempty"`
		Stream          bool   `json:"stream"`
		MaxTokens       int    `json:"max_tokens,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
		ServiceTier     string `json:"service_tier,omitempty"`
		StreamOptions   struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}{Model: m.Model, Stream: true, MaxTokens: req.MaxTokens, ReasoningEffort: m.Effort}
	if m.Fast {
		body.ServiceTier = "fast"
		slog.Debug("service_tier", "value", "fast")
	}
	body.StreamOptions.IncludeUsage = true
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
		Content      string          `json:"content,omitempty"`
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
			push("user", block{Type: "tool_result", ToolUseID: message.ToolCallID, Content: message.Content, IsError: message.IsError})
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
}

func newAccumulator(kind string) *accumulator {
	return &accumulator{kind: kind, calls: map[int]*ToolCall{}, args: map[int]*strings.Builder{}}
}

func (a *accumulator) call(index int) (*ToolCall, *strings.Builder) {
	if a.calls[index] == nil {
		a.calls[index] = &ToolCall{}
		a.args[index] = &strings.Builder{}
	}
	return a.calls[index], a.args[index]
}

func (a *accumulator) feed(data []byte) (piece, error) {
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
		}
		if json.Unmarshal(data, &event) != nil {
			return piece{}, nil
		}
		switch event.Type {
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
		c, args := a.call(tc.Index)
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
	a.reasoning.WriteString(think)
	a.text.WriteString(delta.Content)
	return piece{text: delta.Content, thinking: think}, nil
}

func (a *accumulator) message() Message {
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
