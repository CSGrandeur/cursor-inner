package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind 是模型接口错误的类别。可重试的是限流、服务端、传输、超时和空响应。
type Kind int

const (
	KindOther Kind = iota
	KindRateLimit
	KindServer
	KindTransport
	KindTimeout
	KindEmpty
	KindContextOverflow
	KindAuth
	KindBadRequest
)

// APIError 保留状态码和 Retry-After，供重试判断。
type APIError struct {
	Kind       Kind
	Status     int
	RetryAfter time.Duration
	Text       string
	Detail     string // 上游 error.message（已脱敏、截断约 200 字）
}

type attemptsError struct {
	err     error
	retries int
}

func (e *attemptsError) Error() string { return e.err.Error() }
func (e *attemptsError) Unwrap() error { return e.err }

func withRetries(err error, retries int) error {
	if err == nil || retries <= 0 {
		return err
	}
	return &attemptsError{err: err, retries: retries}
}

// Explain 把模型错误说成控制台能直接读的一句。
func Explain(err error) string {
	if err == nil {
		return ""
	}
	retries := 0
	var tried *attemptsError
	if errors.As(err, &tried) {
		retries = tried.retries
		err = tried.err
	}
	reason := explainKind(err)
	if retries > 0 {
		return fmt.Sprintf("%s，重试 %d 次后失败", reason, retries)
	}
	return reason
}

func explainKind(err error) string {
	if errors.Is(err, context.Canceled) {
		return "已停止"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时"
	}
	var api *APIError
	if errors.As(err, &api) {
		base := ""
		switch api.Kind {
		case KindRateLimit:
			base = "429 限流"
		case KindServer:
			if api.Status > 0 {
				base = fmt.Sprintf("%d 服务端错误", api.Status)
			} else {
				base = "服务端错误"
			}
		case KindTimeout:
			base = "连接超时"
		case KindTransport:
			base = "连接失败"
		case KindEmpty:
			base = "模型没有返回内容"
		case KindAuth:
			base = "密钥被拒绝"
		case KindContextOverflow:
			base = "上下文放不下"
		case KindBadRequest:
			base = "请求被拒绝"
		}
		if base != "" {
			if api.Detail != "" {
				return base + "：" + api.Detail
			}
			return base
		}
	}
	text := err.Error()
	if len(text) > 80 {
		text = text[:80]
	}
	return text
}

func (e *APIError) Error() string {
	if e.Text != "" {
		return e.Text
	}
	return "model request failed"
}

// Overflow 表示上下文放不下，调用方应压缩后再试一次。
func Overflow(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Kind == KindContextOverflow
}

func Retryable(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Kind {
	case KindRateLimit, KindServer, KindTransport, KindTimeout, KindEmpty:
		return true
	default:
		return false
	}
}

func statusError(code int, body []byte, retryAfter string) error {
	text := excerpt(body)
	kind := KindBadRequest
	switch {
	case code == 429:
		kind = KindRateLimit
	case code >= 500:
		kind = KindServer
	case code == 401 || code == 403:
		kind = KindAuth
	case contextOverflow(body):
		kind = KindContextOverflow
	}
	// DashScope 把限流、内容审核、欠费、超长等都塞在 body 的 code/message 里，
	// 有时 HTTP 状态并不对应（例如 400 的 Throttling）。按 code 细分，纠正可重试性判断。
	if k, ok := dashscopeKind(body); ok {
		kind = k
	}
	return &APIError{Kind: kind, Status: code, RetryAfter: parseRetryAfter(retryAfter), Text: fmt.Sprintf("endpoint returned %d: %s", code, text), Detail: upstreamDetail(body)}
}

func transportError(err error) error {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &APIError{Kind: KindTimeout, Text: err.Error()}
	}
	return &APIError{Kind: KindTransport, Text: err.Error()}
}

func emptyError() error {
	return &APIError{Kind: KindEmpty, Text: "endpoint returned no content"}
}

func timeoutError() error {
	return &APIError{Kind: KindTimeout, Text: "stream idle timeout"}
}

var overflowPattern = regexp.MustCompile(`(?i)context length|maximum context|context window|too many tokens|prompt is too long|range of input length|input length|input tokens exceed`)

func contextOverflow(body []byte) bool {
	return overflowPattern.Match(body)
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// dashscopeKind 从 DashScope（含 OpenAI 兼容模式）的错误体里按 code/message 判类别。
// 返回 ok=false 表示没有可识别的 DashScope 线索，调用方沿用按状态码的判断。
func dashscopeKind(body []byte) (Kind, bool) {
	var b struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &b) != nil {
		return KindOther, false
	}
	s := strings.ToLower(firstNonEmpty(b.Code, b.Error.Code, b.Error.Type) + " " + firstNonEmpty(b.Message, b.Error.Message))
	switch {
	case anyContains(s, "throttl", "rate limit", "requests rate", "allocationquota", "flowcontrol", "too many requests", "limit_requests"):
		return KindRateLimit, true
	case anyContains(s, "arrearage", "insufficient_quota", "insufficient balance", "unpurchased", "accessdenied", "no permission"):
		return KindAuth, true
	case anyContains(s, "datainspection", "data_inspection", "responsible_ai", "responsibleai", "content_filter", "contentfilter"):
		return KindBadRequest, true
	case anyContains(s, "range of input length", "maximum context", "input length", "context length", "context window"):
		return KindContextOverflow, true
	case anyContains(s, "requesttimeout", "request time out", "request timed out"):
		return KindTimeout, true
	case anyContains(s, "internalerror", "systemerror", "internal_error", "service unavailable", "serviceunavailable"):
		return KindServer, true
	}
	return KindOther, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func anyContains(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

var secretPattern = regexp.MustCompile(`(?i)sk-[a-z0-9]{6,}|bearer\s+[a-z0-9._\-]{6,}|[a-f0-9]{32,}`)

func redactSecrets(s string) string { return secretPattern.ReplaceAllString(s, "[redacted]") }

// upstreamDetail 从上游错误体取 error.message / message，脱敏并压成单行、截断约 200 字。
func upstreamDetail(body []byte) string {
	var b struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &b) == nil {
		msg = firstNonEmpty(b.Error.Message, b.Message)
	}
	if strings.TrimSpace(msg) == "" {
		msg = string(body)
	}
	msg = strings.Join(strings.Fields(msg), " ")
	msg = redactSecrets(msg)
	r := []rune(msg)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return msg
}
