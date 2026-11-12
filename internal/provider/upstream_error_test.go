package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/runlog"
)

func TestExplainKeepsDataInspectionMessage(t *testing.T) {
	raw := []byte(`{"type":"error","error":{"code":"InternalError.Algo.DataInspectionFailed","message":"<400> InternalError.Algo.DataInspectionFailed: Input text data may contain inappropriate content."}}`)
	err := streamAPIError(raw)
	got := Explain(err)
	if strings.Contains(got, "Input text d") && !strings.Contains(got, "inappropriate content") {
		t.Fatalf("still truncated like the 80-byte bug: %q", got)
	}
	if !strings.Contains(got, "InternalError.Algo.DataInspectionFailed") || !strings.Contains(got, "Input text data may contain inappropriate content") {
		t.Fatalf("missing official code/message: %q", got)
	}
	if !strings.Contains(got, "request_id未知") {
		t.Fatalf("missing request_id unknown: %q", got)
	}
}

func TestParseUpstreamRequestIDFromBodyAndHeader(t *testing.T) {
	body := []byte(`{"error":{"code":"InternalError.Algo.DataInspectionFailed","type":"invalid_request_error","message":"Input text data may contain inappropriate content.","request_id":"body-id-1"},"request_id":"top-id"}`)
	u := parseUpstreamError(body, http.Header{"X-Request-Id": []string{"header-id"}})
	if u.RequestID != "top-id" {
		t.Fatalf("body top request_id should win, got %q", u.RequestID)
	}
	if u.Code != "InternalError.Algo.DataInspectionFailed" || u.Type != "invalid_request_error" {
		t.Fatalf("code/type %+v", u)
	}
	hdr := make(http.Header)
	hdr.Set("X-DashScope-Request-Id", "dash-1")
	onlyHeader := parseUpstreamError([]byte(`{"error":{"message":"no id"}}`), hdr)
	if onlyHeader.RequestID != "dash-1" {
		t.Fatalf("header request id: %+v", onlyHeader)
	}
	missing := parseUpstreamError([]byte(`{"error":{"message":"no id"}}`), nil)
	if missing.RequestID != "" {
		t.Fatalf("must not invent request_id: %+v", missing)
	}
}

func TestStatusErrorHTTPJSONInspection(t *testing.T) {
	body := []byte(`{"error":{"code":"InternalError.Algo.DataInspectionFailed","message":"Input text data may contain inappropriate content."},"request_id":"req-http-1"}`)
	hdr := make(http.Header)
	hdr.Set("x-request-id", "header-should-lose")
	err := statusError(400, body, hdr)
	api, ok := err.(*APIError)
	if !ok {
		t.Fatalf("%T", err)
	}
	if api.Kind != KindBadRequest || api.Status != 400 || api.Code != "InternalError.Algo.DataInspectionFailed" || api.RequestID != "req-http-1" {
		t.Fatalf("%+v", api)
	}
	got := Explain(err)
	if !strings.Contains(got, "请求被拒绝") || !strings.Contains(got, "request_id=req-http-1") || !strings.Contains(got, "inappropriate content") {
		t.Fatalf("explain: %q", got)
	}
}

func TestStreamAPIErrorSSEVariants(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		code string
		id   string
		unk  bool
	}{
		{"error-event", `{"type":"error","error":{"code":"InternalError.Algo.DataInspectionFailed","message":"Input text data may contain inappropriate content.","request_id":"sse-1"}}`, "InternalError.Algo.DataInspectionFailed", "sse-1", false},
		{"response-failed", `{"type":"response.failed","response":{"error":{"code":"InternalError.Algo.DataInspectionFailed","message":"Input text data may contain inappropriate content."},"request_id":"sse-2"}}`, "InternalError.Algo.DataInspectionFailed", "sse-2", false},
		{"message-only-ali-prefix", `{"type":"error","error":{"message":"<400> InternalError.Algo.DataInspectionFailed: Input text data may contain inappropriate content."}}`, "InternalError.Algo.DataInspectionFailed", "", true},
		{"envelope-server-error-hides-nested-code", `{"type":"response.failed","response":{"error":{"code":"server_error","type":"server_error","message":"InternalError.Algo.DataInspectionFailed: Input text data may contain inappropriate content."}}}`, "InternalError.Algo.DataInspectionFailed", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := streamAPIError([]byte(tc.raw))
			api, _ := err.(*APIError)
			if api == nil || api.Kind != KindBadRequest || api.Code != tc.code {
				t.Fatalf("%+v", api)
			}
			if tc.name == "envelope-server-error-hides-nested-code" {
				if api.Type != "server_error" {
					t.Fatalf("type should stay envelope: %+v", api)
				}
				if !ContentInspection(err) {
					t.Fatal("nested inspection code in message must still classify as review")
				}
			}
			got := Explain(err)
			if !strings.Contains(got, "inappropriate content") {
				t.Fatalf("explain: %q", got)
			}
			if tc.name == "envelope-server-error-hides-nested-code" {
				if !strings.Contains(got, "InternalError.Algo.DataInspectionFailed") || !strings.Contains(got, "type=server_error") {
					t.Fatalf("explain should keep nested code and envelope type: %q", got)
				}
			}
			if tc.unk {
				if api.RequestID != "" || !strings.Contains(got, "request_id未知") {
					t.Fatalf("unknown id: %+v %q", api, got)
				}
			} else if api.RequestID != tc.id || !strings.Contains(got, "request_id="+tc.id) {
				t.Fatalf("id=%q explain=%q", api.RequestID, got)
			}
		})
	}
}

func TestExplainChineseUTF8Complete(t *testing.T) {
	msg := "内容审核未通过：输入文本可能包含不当内容，请修改后再试。"
	err := streamAPIError([]byte(`{"type":"error","error":{"code":"DataInspectionFailed","message":"` + msg + `"}}`))
	got := Explain(err)
	if !strings.Contains(got, msg) {
		t.Fatalf("chinese message truncated or missing: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid utf-8")
	}
}

func TestExplainRedactsSecrets(t *testing.T) {
	err := statusError(401, []byte(`{"error":{"message":"invalid key sk-abcdefghijklmnop Bearer abcdefghijklmnop"}}`), nil)
	got := Explain(err)
	if strings.Contains(got, "sk-abcdefghijklmnop") || strings.Contains(strings.ToLower(got), "bearer abcdef") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("expected redaction: %q", got)
	}
}

func TestExplainCompatiblePlainRateLimit(t *testing.T) {
	err := withRetries(&APIError{Kind: KindRateLimit, Status: 429, Text: "endpoint returned 429"}, 2)
	if got := Explain(err); got != "429 限流，重试 2 次后失败" {
		t.Fatalf("%q", got)
	}
}

func TestResponsesChatHTTPJSONErrorIsStructured(t *testing.T) {
	var notes []runlog.Note
	runlog.Set(func(n runlog.Note) { notes = append(notes, n) })
	t.Cleanup(func() { runlog.Set(nil) })
	srv := serveStatus(t, 400, map[string]string{"X-Request-Id": "hdr-1", "Content-Type": "application/json"}, `{"error":{"code":"DataInspectionFailed","message":"Input text data may contain inappropriate content."},"request_id":"body-9"}`)
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "qwen4-max-preview", Effort: "xhigh"}
	_, err := Chat(runlog.WithRequest(context.Background(), "inner-req-1"), m, dialer.Direct(), "sys", []Message{{Role: "user", Content: "hi"}}, []Tool{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}}, func(string) error { return nil }, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	got := Explain(err)
	if !strings.Contains(got, "DataInspectionFailed") || !strings.Contains(got, "inappropriate content") || !strings.Contains(got, "request_id=body-9") {
		t.Fatalf("explain %q", got)
	}
	if strings.Contains(got, "hdr-1") {
		t.Fatalf("body request_id should win: %q", got)
	}
	var saw bool
	for _, n := range notes {
		if n.Kind != "model_error" {
			continue
		}
		saw = true
		if n.Request != "inner-req-1" || n.Code != "DataInspectionFailed" || n.VendorRequestID != "body-9" || n.Status != 400 {
			t.Fatalf("model_error %+v", n)
		}
		if n.Provider != "openai-responses" || n.Effort != "xhigh" || n.Messages < 1 || n.Tools != 1 || n.Roles == "" {
			t.Fatalf("digest %+v", n)
		}
		if strings.Contains(n.Error+n.Roles+n.Detail, "super-secret") {
			t.Fatalf("logged unexpected text: %+v", n)
		}
	}
	if !saw {
		t.Fatalf("notes %+v", notes)
	}
}

func TestResponsesChatSSEErrorAndFailedEvents(t *testing.T) {
	for _, body := range []string{
		sse(`{"type":"error","error":{"code":"DataInspectionFailed","message":"Input text data may contain inappropriate content.","request_id":"sse-err"}}`),
		sse(`{"type":"response.failed","response":{"error":{"code":"DataInspectionFailed","message":"Input text data may contain inappropriate content."},"request_id":"sse-fail"}}`),
	} {
		var seen []byte
		srv := serve(t, body, &seen)
		m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "qwen4-max-preview"}
		_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
		if err == nil {
			t.Fatal("expected stream error")
		}
		got := Explain(err)
		if !strings.Contains(got, "DataInspectionFailed") || !strings.Contains(got, "inappropriate content") {
			t.Fatalf("explain %q", got)
		}
		if !strings.Contains(got, "request_id=sse-err") && !strings.Contains(got, "request_id=sse-fail") {
			t.Fatalf("missing sse request_id: %q", got)
		}
	}
}

func TestEventParseHintDoesNotKeepPayload(t *testing.T) {
	err := json.Unmarshal([]byte(`{"arguments":{"a":1}}`), &struct {
		Arguments string `json:"arguments"`
	}{})
	if eventParseHint(err) != "arguments_not_string" {
		t.Fatalf("hint=%q err=%v", eventParseHint(err), err)
	}
	if peekEventType([]byte(`{"type":"response.output_item.done","item":{"arguments":{}}}`)) != "response.output_item.done" {
		t.Fatal("peek type")
	}
}

func serveStatus(t *testing.T, status int, hdr map[string]string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}
