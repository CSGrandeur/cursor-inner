package provider

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"cursor-inner/internal/config"
	"cursor-inner/internal/runlog"
)

const (
	explainRuneLimit = 500
	detailRuneLimit  = 500
)

var aliErrPrefix = regexp.MustCompile(`^<(\d{3})>\s*([A-Za-z0-9_.]+):\s*([\s\S]*)$`)

// messageCodePrefix 识别写在 message 里的供应商码，例如
// "InternalError.Algo.DataInspectionFailed: Input text data may contain..."。
// 至少要有一段点号/下划线分节，或单独的审核码，避免把 "Error: ..." 这类普通句子当成 code。
var messageCodePrefix = regexp.MustCompile(`(?i)^((?:[A-Za-z][A-Za-z0-9]*[._])+[A-Za-z0-9]+|DataInspectionFailed|ResponseDataInspectionFailed|content_filter):\s+`)

type upstreamErr struct {
	Code, Type, Message, RequestID string
	Status                         int
}

// parseUpstreamError 从 HTTP JSON 体和官方响应头取出 code/message/type/request_id。
// 响应头仅认常见官方字段，不编造。body 中的 request_id 优先于响应头。
func parseUpstreamError(body []byte, hdr http.Header) upstreamErr {
	u := upstreamErr{}
	if len(body) > 0 {
		var b struct {
			Code      string `json:"code"`
			Type      string `json:"type"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			RequestId string `json:"requestId"`
			Error     struct {
				Code      string `json:"code"`
				Type      string `json:"type"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
				RequestId string `json:"requestId"`
			} `json:"error"`
			Response struct {
				RequestID string `json:"request_id"`
				RequestId string `json:"requestId"`
				Error     struct {
					Code      string `json:"code"`
					Type      string `json:"type"`
					Message   string `json:"message"`
					RequestID string `json:"request_id"`
					RequestId string `json:"requestId"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(body, &b) == nil {
			u.Code = firstNonEmpty(b.Error.Code, b.Response.Error.Code, b.Code)
			u.Type = firstNonEmpty(b.Error.Type, b.Response.Error.Type, b.Type)
			u.Message = firstNonEmpty(b.Error.Message, b.Response.Error.Message, b.Message)
			u.RequestID = firstNonEmpty(
				b.RequestID, b.RequestId,
				b.Error.RequestID, b.Error.RequestId,
				b.Response.RequestID, b.Response.RequestId,
				b.Response.Error.RequestID, b.Response.Error.RequestId,
			)
		}
	}
	if strings.TrimSpace(u.Message) == "" && len(body) > 0 && !json.Valid(bytesTrim(body)) {
		u.Message = string(body)
	}
	fillFromAliPrefix(&u)
	liftNestedCode(&u)
	if u.RequestID == "" && hdr != nil {
		u.RequestID = requestIDFromHeader(hdr)
	}
	u.Code = strings.TrimSpace(u.Code)
	u.Type = strings.TrimSpace(u.Type)
	u.Message = strings.Join(strings.Fields(redactSecrets(u.Message)), " ")
	u.RequestID = strings.TrimSpace(redactSecrets(u.RequestID))
	return u
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func fillFromAliPrefix(u *upstreamErr) {
	m := strings.TrimSpace(u.Message)
	if m == "" {
		return
	}
	g := aliErrPrefix.FindStringSubmatch(m)
	if len(g) != 4 {
		return
	}
	if u.Status == 0 {
		u.Status, _ = strconv.Atoi(g[1])
	}
	if u.Code == "" || genericEnvelopeCode(u.Code) {
		if u.Type == "" && genericEnvelopeCode(u.Code) {
			u.Type = u.Code
		}
		u.Code = g[2]
	}
}

// genericEnvelopeCode 是 SSE/OpenAI 外壳上的笼统 type/code（如 server_error）。
// 真值审核码若只出现在 message 里，不能让此外壳盖掉。
func genericEnvelopeCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "server_error", "invalid_request_error", "api_error", "internal_error", "bad_request", "internalerror":
		return true
	default:
		return false
	}
}

func liftNestedCode(u *upstreamErr) {
	if u == nil {
		return
	}
	g := messageCodePrefix.FindStringSubmatch(strings.TrimSpace(u.Message))
	if len(g) != 2 {
		return
	}
	nested := g[1]
	if u.Code == "" || genericEnvelopeCode(u.Code) {
		if u.Type == "" && genericEnvelopeCode(u.Code) {
			u.Type = u.Code
		}
		u.Code = nested
	}
}

func requestIDFromHeader(hdr http.Header) string {
	for _, key := range []string{
		"X-Request-Id",
		"X-DashScope-Request-Id",
		"Request-Id",
		"X-OpenAI-Request-Id",
	} {
		if v := strings.TrimSpace(hdr.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

func statusError(code int, body []byte, hdr http.Header) error {
	retryAfter := ""
	if hdr != nil {
		retryAfter = hdr.Get("Retry-After")
	}
	u := parseUpstreamError(body, hdr)
	if u.Status == 0 {
		u.Status = code
	}
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
	if k, ok := dashscopeKind(body); ok {
		kind = k
	} else if k, ok := classifyDashscopeText(u.Code + " " + u.Message); ok {
		kind = k
	}
	detail := u.Message
	if detail == "" {
		detail = upstreamDetail(body)
	} else {
		detail = clipRunes(detail, detailRuneLimit)
	}
	text := fmt.Sprintf("endpoint returned %d: %s", code, excerpt(body))
	return &APIError{
		Kind:       kind,
		Status:     code,
		RetryAfter: parseRetryAfter(retryAfter),
		Text:       text,
		Detail:     detail,
		Code:       u.Code,
		Type:       u.Type,
		RequestID:  u.RequestID,
	}
}

// streamAPIError 把 Responses / Chat Completions / Anthropic 的 SSE 错误事件收成 APIError，
// 不再走纯字符串，避免 Explain 按字节截断、丢掉 code 与 request_id。
func streamAPIError(raw []byte) error {
	u := parseUpstreamError(raw, nil)
	status := u.Status
	kind := KindOther
	if k, ok := dashscopeKind(raw); ok {
		kind = k
	} else if k, ok := classifyDashscopeText(u.Code + " " + u.Message); ok {
		kind = k
	}
	if status == 429 {
		kind = KindRateLimit
	} else if status == 401 || status == 403 {
		if kind == KindOther {
			kind = KindAuth
		}
	} else if status >= 500 {
		if kind == KindOther {
			kind = KindServer
		}
	} else if status >= 400 || u.Code != "" || u.Message != "" {
		if kind == KindOther {
			kind = KindBadRequest
		}
	}
	detail := clipRunes(u.Message, detailRuneLimit)
	text := "endpoint stream error"
	if u.Message != "" {
		text = "endpoint stream error: " + detail
	}
	return &APIError{
		Kind:      kind,
		Status:    status,
		Text:      text,
		Detail:    detail,
		Code:      u.Code,
		Type:      u.Type,
		RequestID: u.RequestID,
	}
}

func clipRunes(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit]) + "…"
}

func formatAPIError(api *APIError) string {
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
		if api.Compacted {
			base = "上游空回复，可能上下文过大，已尝试压缩"
		}
	case KindAuth:
		base = "密钥被拒绝"
	case KindContextOverflow:
		base = "上下文放不下"
	case KindBadRequest:
		base = "请求被拒绝"
	}
	body := composeUpstream(api.Status, api.Code, api.Type, api.Detail)
	out := ""
	if base != "" && body != "" {
		out = base + "：" + body
	} else if base != "" {
		out = base
	} else if body != "" {
		out = body
	} else {
		out = api.Text
	}
	if api.Code != "" || api.Detail != "" || api.RequestID != "" || (api.Status >= 400 && (api.Kind == KindBadRequest || api.Kind == KindServer || api.Kind == KindAuth)) {
		if api.RequestID != "" {
			out += " (request_id=" + api.RequestID + ")"
		} else {
			out += " (request_id未知)"
		}
	}
	return clipRunes(out, explainRuneLimit)
}

func composeUpstream(status int, code, typ, msg string) string {
	msg = strings.TrimSpace(msg)
	if aliErrPrefix.MatchString(msg) {
		return msg
	}
	if msg == "" && strings.TrimSpace(code) == "" && strings.TrimSpace(typ) == "" {
		return ""
	}
	var b strings.Builder
	if status > 0 {
		fmt.Fprintf(&b, "<%d>", status)
	}
	label := firstNonEmpty(code, typ)
	if label != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(label)
		if typ != "" && !strings.EqualFold(strings.TrimSpace(typ), strings.TrimSpace(label)) {
			fmt.Fprintf(&b, " (type=%s)", typ)
		}
	}
	if msg != "" {
		if b.Len() > 0 {
			b.WriteString(": ")
		}
		b.WriteString(msg)
	}
	return b.String()
}

func describeRequest(m config.Model, req chatRequest) runlog.Note {
	counts := map[string]int{}
	order := make([]string, 0, 4)
	add := func(role string) {
		role = strings.TrimSpace(role)
		if role == "" {
			return
		}
		if _, ok := counts[role]; !ok {
			order = append(order, role)
		}
		counts[role]++
	}
	runes := 0
	if strings.TrimSpace(req.System) != "" {
		add("system")
		runes += utf8.RuneCountInString(req.System)
	}
	for _, msg := range req.Messages {
		add(msg.Role)
		runes += utf8.RuneCountInString(msg.Content)
	}
	parts := make([]string, 0, len(order))
	for _, role := range order {
		parts = append(parts, fmt.Sprintf("%s:%d", role, counts[role]))
	}
	return runlog.Note{
		Provider: m.Type,
		Effort:   requestEffort(req, m),
		Roles:    strings.Join(parts, ","),
		Messages: len(req.Messages),
		Tools:    len(req.Tools),
		Chars:    runes,
	}
}

func mergeDigest(n runlog.Note, d runlog.Note) runlog.Note {
	if n.Provider == "" {
		n.Provider = d.Provider
	}
	if n.Effort == "" {
		n.Effort = d.Effort
	}
	if n.Roles == "" {
		n.Roles = d.Roles
	}
	if n.Messages == 0 {
		n.Messages = d.Messages
	}
	if n.Tools == 0 {
		n.Tools = d.Tools
	}
	if n.Chars == 0 {
		n.Chars = d.Chars
	}
	return n
}

func peekEventType(data []byte) string {
	var b struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &b) != nil {
		return ""
	}
	return strings.TrimSpace(b.Type)
}

func eventParseHint(err error) string {
	if err == nil {
		return "unmarshal_failed"
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "arguments") && strings.Contains(s, "string") {
		return "arguments_not_string"
	}
	return "unmarshal_failed"
}

func logEventParseError(data []byte, err error) {
	hint := eventParseHint(err)
	slog.Info("responses_event_ignored 事件无法按既有结构解析，已跳过（不记录正文）",
		"event_type", peekEventType(data), "reason", hint)
}
