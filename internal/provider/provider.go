package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

const testPrompt = "Output the numbers 1 through 120 separated by a single space. No commas, no newlines, no explanation."

type Result struct {
	OK                   bool      `json:"ok"`
	Status               int       `json:"status"`
	DurationMS           int64     `json:"duration_ms"`
	FirstValidResponseMS *int64    `json:"first_valid_response_ms,omitempty"`
	OutputTokens         uint64    `json:"output_tokens"`
	TokensPerSecond      float64   `json:"tokens_per_second"`
	TokensEstimated      bool      `json:"tokens_estimated"`
	Output               string    `json:"output"`
	At                   string    `json:"at,omitempty"`
	Error                i18n.Text `json:"error,omitzero"`
}

func (r Result) LastTest() config.LastTest {
	return config.LastTest{
		OK:                   r.OK,
		At:                   r.At,
		DurationMS:           r.DurationMS,
		FirstValidResponseMS: r.FirstValidResponseMS,
		OutputTokens:         r.OutputTokens,
		TokensPerSecond:      r.TokensPerSecond,
		TokensEstimated:      r.TokensEstimated,
		Output:               r.Output,
		Error:                r.Error,
	}
}

func Prepare(m config.Model) (config.Model, error) {
	m.DisplayName = strings.TrimSpace(m.DisplayName)
	m.Type = strings.TrimSpace(m.Type)
	m.BaseURL = strings.TrimSpace(m.BaseURL)
	m.APIKey = strings.TrimSpace(m.APIKey)
	m.Model = strings.TrimSpace(m.Model)
	if m.DisplayName == "" || m.BaseURL == "" || m.APIKey == "" || m.Model == "" {
		return m, i18n.E("显示名、接口地址、密钥和模型名都要填", "Display name, endpoint URL, API key and model name are all required")
	}
	if m.Type != "openai-chat" && m.Type != "anthropic" {
		return m, i18n.E("接口类型只支持 openai-chat 和 anthropic", "Endpoint type must be openai-chat or anthropic")
	}
	if _, err := RequestURL(m); err != nil {
		return m, err
	}
	m.ID = hashID(m)
	return m, nil
}

func hashID(m config.Model) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{m.Type, m.BaseURL, m.Model, m.APIKey, m.DisplayName}, "\n")))
	return hex.EncodeToString(sum[:8])
}

func RequestURL(m config.Model) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", i18n.E("接口地址需要以 http 或 https 开头", "Endpoint URL must start with http or https")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	switch m.Type {
	case "openai-chat":
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			return base, nil
		case strings.HasSuffix(path, "/v1"):
			return base + "/chat/completions", nil
		default:
			return base + "/v1/chat/completions", nil
		}
	case "anthropic":
		switch {
		case strings.HasSuffix(path, "/messages"):
			return base, nil
		case strings.HasSuffix(path, "/v1"):
			return base + "/messages", nil
		default:
			return base + "/v1/messages", nil
		}
	default:
		return "", i18n.E("接口类型只支持 openai-chat 和 anthropic", "Endpoint type must be openai-chat or anthropic")
	}
}

func Test(ctx context.Context, m config.Model, dial dialer.Func) Result {
	start := time.Now()
	result := Result{At: start.UTC().Format(time.RFC3339)}
	finish := func() Result {
		elapsed := time.Since(start)
		result.DurationMS = elapsed.Milliseconds()
		if result.OutputTokens == 0 && result.Output != "" {
			result.OutputTokens = estimateOutputTokens(result.Output)
			result.TokensEstimated = true
		}
		if elapsed > 0 && result.OutputTokens > 0 {
			result.TokensPerSecond = float64(result.OutputTokens) / elapsed.Seconds()
		}
		return result
	}
	resp, err := do(ctx, m, dial, chatRequest{Messages: []Message{{Role: "user", Content: testPrompt}}, MaxTokens: 2048})
	if err != nil {
		result.Error = i18n.Of(err)
		return finish()
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	rawHead := make([]byte, 1)
	n, err := resp.Body.Read(rawHead)
	if n == 0 && err != nil {
		result.Error = i18n.Of(err)
		return finish()
	}
	rest := io.MultiReader(bytes.NewReader(rawHead[:n]), resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(rest, 4096))
		result.Error = i18n.Tf("接口返回 %d：%s", "Endpoint returned %d: %s", resp.StatusCode, excerpt(body))
		return finish()
	}
	var output strings.Builder
	var usage uint64
	record := func(text string) {
		if result.FirstValidResponseMS == nil {
			ms := time.Since(start).Milliseconds()
			result.FirstValidResponseMS = &ms
		}
		output.WriteString(text)
	}
	if n > 0 && rawHead[0] == '{' {
		body, err := io.ReadAll(io.LimitReader(rest, 8<<20))
		if err != nil {
			result.Error = i18n.Of(err)
			return finish()
		}
		text, err := staticText(body, m.Type)
		if err != nil {
			result.Error = i18n.Of(err)
			return finish()
		}
		if text == "" {
			result.Error = i18n.T("接口没有返回文本", "Endpoint returned no text")
			return finish()
		}
		record(text)
		if tokens, ok := staticUsage(body); ok {
			usage = tokens
		}
	} else {
		sc := bufio.NewScanner(rest)
		sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
		for sc.Scan() {
			event := parseSSE(sc.Text(), m.Type)
			if event.textOK {
				record(event.text)
			}
			if event.usageOK && event.usage > usage {
				usage = event.usage
			}
		}
		if err := sc.Err(); err != nil {
			result.Error = i18n.Of(err)
			return finish()
		}
	}
	result.Output = strings.TrimSpace(output.String())
	if result.FirstValidResponseMS == nil || result.Output == "" {
		result.Error = i18n.T("没有收到有效输出", "No valid output received")
		return finish()
	}
	if usage > 0 {
		result.OutputTokens = usage
	}
	result.OK = true
	return finish()
}

func deltaLine(line, kind string) (string, bool) {
	event := parseSSE(line, kind)
	return event.text, event.textOK && event.text != ""
}

type sseEvent struct {
	text    string
	textOK  bool
	usage   uint64
	usageOK bool
}

func parseSSE(line, kind string) sseEvent {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return sseEvent{}
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return sseEvent{}
	}
	if kind == "anthropic" {
		var event struct {
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens uint64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			return sseEvent{}
		}
		out := sseEvent{text: event.Delta.Text, textOK: event.Delta.Text != ""}
		if event.Usage != nil && event.Usage.OutputTokens > 0 {
			out.usage = event.Usage.OutputTokens
			out.usageOK = true
		}
		return out
	}
	var event struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			CompletionTokens uint64 `json:"completion_tokens"`
			OutputTokens     uint64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &event) != nil {
		return sseEvent{}
	}
	out := sseEvent{}
	if len(event.Choices) > 0 {
		out.text = event.Choices[0].Delta.Content
		out.textOK = true
	}
	if event.Usage != nil {
		tokens := event.Usage.CompletionTokens
		if event.Usage.OutputTokens > tokens {
			tokens = event.Usage.OutputTokens
		}
		if tokens > 0 {
			out.usage = tokens
			out.usageOK = true
		}
	}
	return out
}

func staticText(raw []byte, kind string) (string, error) {
	if kind == "anthropic" {
		var body struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return "", err
		}
		if body.Error.Message != "" {
			return "", errors.New(body.Error.Message)
		}
		var b strings.Builder
		for _, part := range body.Content {
			b.WriteString(part.Text)
		}
		return b.String(), nil
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if body.Error.Message != "" {
		return "", errors.New(body.Error.Message)
	}
	if len(body.Choices) == 0 {
		return "", nil
	}
	return body.Choices[0].Message.Content, nil
}

func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if utf8.RuneCountInString(s) <= 240 {
		return s
	}
	return string([]rune(s)[:240])
}

func staticUsage(raw []byte) (uint64, bool) {
	var body struct {
		Usage struct {
			CompletionTokens uint64 `json:"completion_tokens"`
			OutputTokens     uint64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return 0, false
	}
	tokens := body.Usage.CompletionTokens
	if body.Usage.OutputTokens > tokens {
		tokens = body.Usage.OutputTokens
	}
	return tokens, tokens > 0
}

func estimateOutputTokens(output string) uint64 {
	words := uint64(len(strings.Fields(output)))
	if words > 0 {
		return words
	}
	if output == "" {
		return 0
	}
	n := uint64(utf8.RuneCountInString(output))
	return (n + 3) / 4
}
