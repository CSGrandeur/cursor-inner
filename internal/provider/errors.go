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
	Detail     string // 上游 error.message（已脱敏，按 rune 截断）
	Code       string // 上游 error.code，如 InternalError.Algo.DataInspectionFailed
	Type       string // 上游 error.type
	RequestID  string // 供应商 request_id；空表示未知，不编造
	// Large 表示空回复发生在带了很多图片的请求上，调用方可以省略一批旧图片后再试一次。
	Large bool
	// Compacted 表示已经省略旧图片重试过，仍然空回复。
	Compacted bool
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
		if s := formatAPIError(api); s != "" {
			return s
		}
	}
	return clipRunes(err.Error(), explainRuneLimit)
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

// inspectionPattern 识别供应商内容审核拒绝（含 DashScope DataInspection 的多种拼写）。
// 与 Qwen Code 的永久失败码对齐，但不把鉴权/欠费/超长算进审核。
var inspectionPattern = regexp.MustCompile(`(?i)data[_-]?inspection|content[_-]?filter|responsible[_-]?ai|inappropriate content|prohibited content|内容审核|不当内容`)

// ContentInspection 判断错误是否为内容审核拒绝。取消、传输、超时不得算作审核。
// 即使上游把 SSE 标成 server_error，只要 code/message 指向审核，也按审核处理。
func ContentInspection(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var api *APIError
	if !errors.As(err, &api) {
		return inspectionPattern.MatchString(err.Error())
	}
	switch api.Kind {
	case KindTransport, KindTimeout, KindRateLimit, KindContextOverflow, KindEmpty, KindAuth:
		return false
	}
	s := strings.ToLower(strings.Join([]string{api.Code, api.Type, api.Text, api.Detail}, " "))
	return inspectionPattern.MatchString(s)
}

func Retryable(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	if ContentInspection(err) {
		return false
	}
	switch api.Kind {
	case KindRateLimit, KindServer, KindTransport, KindTimeout, KindEmpty:
		return true
	default:
		return false
	}
}

// wantsEffortFallback 判断 400 是不是在拒绝 reasoning_effort 的档位。
// 有的端点只接受固定的档位集合（例如 low/high/max），Cursor 传来的档位可能不在里面；
// 去掉字段后由端点用默认档。只认错误里点名 reasoning_effort 或其变体的 400，
// 且拒绝词要出现在字段名附近，避免把别的参数的枚举错误也当成档位问题。
func wantsEffortFallback(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	if api.Status != 400 && api.Kind != KindBadRequest {
		return false
	}
	text := strings.ToLower(api.Text + " " + api.Detail + " " + api.Code)
	for _, token := range []string{"reasoning_effort", "reasoning effort", "reasoning level", "reasoning.effort"} {
		at := strings.Index(text, token)
		if at < 0 {
			continue
		}
		near := text[max(0, at-64):min(len(text), at+len(token)+96)]
		for _, marker := range []string{
			"must be one of", "expected one of", "invalid", "unsupported",
			"not supported", "unknown", "unrecognized", "not permitted",
		} {
			if strings.Contains(near, marker) {
				return true
			}
		}
	}
	return false
}

// DashScope 把限流、内容审核、欠费、超长等都塞在 body 的 code/message 里，
// 有时 HTTP 状态并不对应（例如 400 的 Throttling）。按 code 细分，纠正可重试性判断。
// statusError 见 upstream_error.go。

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

// dataIdleText 也是诊断日志的检索关键字。
const dataIdleText = "upstream_data_idle: stream sent only keepalives, no data"

func dataIdleError() error {
	return &APIError{Kind: KindTimeout, Text: dataIdleText}
}

// IsDataIdle 判断错误是否为“只收到 keepalive、没有 data 帧”的超时。
func IsDataIdle(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Kind == KindTimeout && api.Text == dataIdleText
}

// EmptyLarge 判断是否为“带很多图片的请求得到空回复”。
func EmptyLarge(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Kind == KindEmpty && api.Large
}

// MarkCompacted 标记空回复发生在省略旧图片重试之后，给用户的说明随之改变。
func MarkCompacted(err error) error {
	var api *APIError
	if errors.As(err, &api) && api.Kind == KindEmpty {
		api.Compacted = true
	}
	return err
}

// IsEmpty 判断错误是否为上游正常结束却没有任何内容。
func IsEmpty(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Kind == KindEmpty
}

func isTimeout(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Kind == KindTimeout
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
	s := firstNonEmpty(b.Code, b.Error.Code, b.Error.Type) + " " + firstNonEmpty(b.Message, b.Error.Message)
	return classifyDashscopeText(s)
}

func classifyDashscopeText(s string) (Kind, bool) {
	s = strings.ToLower(s)
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
	return clipRunes(msg, detailRuneLimit)
}
