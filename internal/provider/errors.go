package provider

import (
	"context"
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
		switch api.Kind {
		case KindRateLimit:
			return "429 限流"
		case KindServer:
			if api.Status > 0 {
				return fmt.Sprintf("%d 服务端错误", api.Status)
			}
			return "服务端错误"
		case KindTimeout:
			return "连接超时"
		case KindTransport:
			return "连接失败"
		case KindEmpty:
			return "模型没有返回内容"
		case KindAuth:
			return "密钥被拒绝"
		case KindContextOverflow:
			return "上下文放不下"
		case KindBadRequest:
			return "请求被拒绝"
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
	return &APIError{Kind: kind, Status: code, RetryAfter: parseRetryAfter(retryAfter), Text: fmt.Sprintf("endpoint returned %d: %s", code, text)}
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

var overflowPattern = regexp.MustCompile(`(?i)context length|maximum context|context window|too many tokens|prompt is too long`)

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
