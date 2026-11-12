package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
)

func TestProbeRequestModelCapsLargeMaxOutput(t *testing.T) {
	got := probeRequestModel(config.Model{MaxOutputTokens: 256000})
	if got.MaxOutputTokens != probeMaxOutputTokens {
		t.Fatalf("256000 -> %d, want %d", got.MaxOutputTokens, probeMaxOutputTokens)
	}
	if probeMaxOutputTokens != 8192 {
		t.Fatalf("probe budget = %d, want 8192 (thinking+output shared, official min 16)", probeMaxOutputTokens)
	}
	got = probeRequestModel(config.Model{MaxOutputTokens: 0})
	if got.MaxOutputTokens != probeMaxOutputTokens {
		t.Fatalf("0 -> %d, want %d", got.MaxOutputTokens, probeMaxOutputTokens)
	}
	got = probeRequestModel(config.Model{MaxOutputTokens: 512})
	if got.MaxOutputTokens != 512 {
		t.Fatalf("smaller budget should be kept, got %d", got.MaxOutputTokens)
	}
}

func TestProbeReasoningEffortForQwen4Only(t *testing.T) {
	if got := probeReasoningEffort("qwen4-max-preview"); got != "low" {
		t.Fatalf("qwen4-max-preview: %q", got)
	}
	if got := probeReasoningEffort("openai/qwen3.8-max"); got != "low" {
		t.Fatalf("qwen3.8-max: %q", got)
	}
	if got := probeReasoningEffort("qwen3-max"); got != "" {
		t.Fatalf("qwen3-max should not inherit qwen4 effort, got %q", got)
	}
	if got := probeReasoningEffort("deepseek-v4.1-flash-1017"); got != "" {
		t.Fatalf("deepseek should not get qwen effort, got %q", got)
	}
}

func responsesSSE(events ...string) string { return sse(events...) }

func textOKSSE() string {
	return responsesSSE(
		`{"type":"response.output_text.delta","delta":"OK"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
}

func pingToolSSE() string {
	return responsesSSE(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"ping_probe","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"x\":\"hi\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"ping_probe","arguments":"{\"x\":\"hi\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
}

func TestProbeToolsCapsMaxOutputAndOfficialShape(t *testing.T) {
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, pingToolSSE())
	}))
	t.Cleanup(srv.Close)

	m := config.Model{
		Type:            "openai-responses",
		BaseURL:         srv.URL + "/v1",
		APIKey:          "k",
		Model:           "qwen4-max-preview",
		MaxOutputTokens: 256000,
		ContextWindow:   1000000,
	}
	ok, err := probeTools(context.Background(), m, dialer.Direct())
	if err != nil || !ok {
		t.Fatalf("probeTools: ok=%v err=%v body=%s", ok, err, seen)
	}
	var body map[string]any
	if err := json.Unmarshal(seen, &body); err != nil {
		t.Fatal(err)
	}
	if int(body["max_output_tokens"].(float64)) != probeMaxOutputTokens {
		t.Fatalf("max_output_tokens=%v body=%s", body["max_output_tokens"], seen)
	}
	if strings.Contains(string(seen), "256000") {
		t.Fatalf("configured max output leaked onto the wire: %s", seen)
	}
	if body["include"] != nil {
		t.Fatalf("probe Responses request should omit include: %s", seen)
	}
	if body["tool_choice"] != "required" {
		t.Fatalf("tool_choice=%v", body["tool_choice"])
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Fatalf("reasoning.effort=%v body=%s", body["reasoning"], seen)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%s", seen)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "ping_probe" || tool["function"] != nil || tool["parameters"] == nil {
		t.Fatalf("probe tools should be official flat schema: %s", seen)
	}
}

func TestProductionResponsesBodyUnchangedWithoutProbeOpts(t *testing.T) {
	m := config.Model{Type: "openai-responses", Model: "qwen4-max-preview", PromptCacheKey: ""}
	raw, err := responsesBody(m, chatRequest{
		Messages:  []Message{{Role: "user", Content: "hi"}},
		Tools:     []Tool{{Name: "Read", Description: "r", Parameters: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	inc, _ := body["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("production include missing: %s", raw)
	}
	if body["reasoning"] != nil || body["tool_choice"] != nil {
		t.Fatalf("production body unexpectedly has probe fields: %s", raw)
	}
	tools, _ := body["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "Read" || tool["function"] != nil || tool["parameters"] == nil {
		t.Fatalf("production Responses tools should be official flat schema: %s", raw)
	}
}

func hangHandler(started chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func TestProbeGenerateTimeoutSkipsLaterStagesAsUnknown(t *testing.T) {
	started := make(chan struct{}, 4)
	srv := httptest.NewServer(hangHandler(started))
	t.Cleanup(srv.Close)

	m := config.Model{
		Type:            "openai-responses",
		BaseURL:         srv.URL + "/v1",
		APIKey:          "k",
		Model:           "probe-timeout",
		MaxOutputTokens: 256000,
		Reasoning:       true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	var stages []string
	result, updated := Probe(ctx, m, dialer.Direct(), func(p Progress) {
		if p.Stage != "" {
			stages = append(stages, p.Stage+":"+p.State)
		}
	})
	if result.OK {
		t.Fatal("expected probe failure after generate timeout")
	}
	joined := strings.ToLower(result.Error.En + result.Error.Zh + result.Error.String())
	if !strings.Contains(joined, "超时") && !strings.Contains(joined, "timeout") && !strings.Contains(joined, "deadline") && !strings.Contains(joined, "未测出") && !strings.Contains(joined, "not determined") {
		t.Fatalf("error should be a test exception / not determined, got %#v", result.Error)
	}
	if strings.Contains(result.Error.Zh, "不支持") || strings.Contains(strings.ToLower(result.Error.En), "not supported") {
		t.Fatalf("must not call the model unsupported: %#v", result.Error)
	}
	if result.DurationMS < 80 {
		t.Fatalf("DurationMS=%d", result.DurationMS)
	}
	joinedStages := strings.Join(stages, ",")
	if !strings.Contains(joinedStages, "generate:start") || !strings.Contains(joinedStages, "generate:waiting_headers") {
		t.Fatalf("stages=%v", stages)
	}
	if strings.Contains(joinedStages, "tools:start") || strings.Contains(joinedStages, "reasoning:start") || strings.Contains(joinedStages, "speed:start") {
		t.Fatalf("later stages should be skipped, not started: %v", stages)
	}
	if !strings.Contains(joinedStages, "tools:skipped") {
		t.Fatalf("tools should be skipped: %v", stages)
	}
	caps := result.Capabilities
	if caps == nil || caps.ToolsStatus != config.CapUnknown || caps.Tools || caps.Generate {
		t.Fatalf("timeout must be unknown, not false-unsupported: %+v", caps)
	}
	if caps.ReasoningStatus != config.CapUnknown || caps.Reasoning {
		t.Fatalf("reasoning after generate timeout: %+v", caps)
	}
	if updated.MaxOutputTokens != 256000 {
		t.Fatalf("returned model max output was rewritten to %d", updated.MaxOutputTokens)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("server was never reached")
	}
}

func TestProbeEndpointHostOmitsPath(t *testing.T) {
	if got := probeEndpointHost("https://token-plan.example.com/compatible-mode/v1"); got != "token-plan.example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestMergeCapabilitiesProtectsConfirmedFromUnknown(t *testing.T) {
	prev := &config.Capabilities{Tools: true, ToolsStatus: config.CapConfirmed, Reasoning: true, ReasoningStatus: config.CapConfirmed}
	next := &config.Capabilities{ToolsStatus: config.CapUnknown, ReasoningStatus: config.CapUnknown, ImagesStatus: config.CapUnknown}
	got := MergeCapabilities(prev, next)
	if !got.Tools || got.ToolsStatus != config.CapConfirmed || !got.Reasoning || got.ReasoningStatus != config.CapConfirmed {
		t.Fatalf("unknown must not overwrite confirmed: %+v", got)
	}
	rejected := MergeCapabilities(prev, &config.Capabilities{ToolsStatus: config.CapRejected, ToolsNote: i18n.T("该接口拒绝此测试方式", "The endpoint rejected this test method")})
	if rejected.Tools || rejected.ToolsStatus != config.CapRejected {
		t.Fatalf("explicit rejection may update confirmed: %+v", rejected)
	}
}

func TestClassifyProbeErrDoesNotMeanUnsupported(t *testing.T) {
	st, note := classifyProbeErr(context.DeadlineExceeded)
	if st != config.CapUnknown || !strings.Contains(note.Zh, "未测出") {
		t.Fatalf("timeout: %s %#v", st, note)
	}
	st, note = classifyProbeErr(&APIError{Kind: KindAuth, Status: 401, Text: "no key"})
	if st != config.CapUnknown || !strings.Contains(note.Zh, "鉴权") {
		t.Fatalf("401: %s %#v", st, note)
	}
	st, note = classifyProbeErr(&APIError{Kind: KindRateLimit, Status: 429, Text: "rate"})
	if st != config.CapUnknown || !strings.Contains(note.Zh, "限流") {
		t.Fatalf("429: %s %#v", st, note)
	}
	st, note = classifyProbeErr(&APIError{Kind: KindServer, Status: 503, Text: "busy"})
	if st != config.CapUnknown || !strings.Contains(note.Zh, "上游") {
		t.Fatalf("5xx: %s %#v", st, note)
	}
	st, note = classifyProbeErr(&APIError{Kind: KindBadRequest, Status: 400, Text: "unknown param"})
	if st != config.CapRejected || !strings.Contains(note.Zh, "拒绝") {
		t.Fatalf("400: %s %#v", st, note)
	}
}

func TestProbeReasoningOnlyGenerateCountsAsSuccess(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(body), "ping_probe") {
			_, _ = io.WriteString(w, responsesSSE(
				`{"type":"response.output_text.delta","delta":"I will not call tools."}`,
				`{"type":"response.completed","response":{"status":"completed"}}`,
			))
			return
		}
		_, _ = io.WriteString(w, responsesSSE(
			`{"type":"response.reasoning_text.delta","delta":"thinking"}`,
			`{"type":"response.completed","response":{"usage":{"output_tokens":4},"status":"completed"}}`,
		))
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", MaxOutputTokens: 100}
	result, _ := Probe(context.Background(), m, dialer.Direct(), nil)
	if result.Capabilities == nil || !result.Capabilities.Generate || result.Capabilities.GenerateStatus != config.CapConfirmed {
		t.Fatalf("reasoning-only stream should confirm generation: ok=%v caps=%+v err=%#v", result.OK, result.Capabilities, result.Error)
	}
	if result.Capabilities.Tools || result.Capabilities.ToolsStatus != config.CapUnknown {
		t.Fatalf("no tool call must be unknown, not unsupported: %+v", result.Capabilities)
	}
	if n < 2 {
		t.Fatalf("expected generate then tools, requests=%d", n)
	}
}

func TestProbeCancelStopsGenerate(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	result, _ := Probe(ctx, m, dialer.Direct(), nil)
	if result.OK {
		t.Fatal("canceled probe should not be ok")
	}
	if result.Capabilities != nil && result.Capabilities.ToolsStatus == config.CapConfirmed {
		t.Fatalf("cancel must not confirm tools: %+v", result.Capabilities)
	}
}

func TestProbeBadRequestIsRejectedNotUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"parameter reasoning.effort is not supported"}}`)
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "other-model"}
	result, updated := Probe(context.Background(), m, dialer.Direct(), nil)
	if result.OK {
		t.Fatal("expected generate not to confirm")
	}
	if result.Capabilities == nil || result.Capabilities.GenerateStatus != config.CapRejected {
		t.Fatalf("400 should be rejected test method, got %+v", result.Capabilities)
	}
	if !strings.Contains(result.Capabilities.GenerateNote.Zh, "拒绝") {
		t.Fatalf("note=%#v", result.Capabilities.GenerateNote)
	}
	if updated.Reasoning {
		// model had Reasoning false by default; just ensure we didn't invent support language
	}
}

func TestStageCtxDoesNotStarveParent(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ctx1, cancel1 := stageCtx(parent, 50*time.Millisecond)
	<-ctx1.Done()
	cancel1()
	if parent.Err() != nil {
		t.Fatal("child budget should not cancel the parent")
	}
	ctx2, cancel2 := stageCtx(parent, time.Second)
	defer cancel2()
	if dl, ok := ctx2.Deadline(); !ok || time.Until(dl) > 200*time.Millisecond {
		t.Fatalf("child should be clipped to parent remain, deadline=%v", dl)
	}
}
