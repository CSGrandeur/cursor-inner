package mitm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/protox"
)

func TestInlineHintKeepsModelNames(t *testing.T) {
	if got, ok := inlineHint([]byte("default")); !ok || got != "default" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := inlineHint([]byte("2bfba7a3015e5fe69802fd806e3981b1132706799ccc232d27bd03a1bdc887fb")); ok {
		t.Fatal("hash")
	}
	if _, ok := inlineHint([]byte("Always respond in Chinese-simplified")); ok {
		t.Fatal("sentence")
	}
}

func TestPromptPrefersTheShortInstruction(t *testing.T) {
	body := protox.AppendString(nil, 1, strings.Repeat("rule text ", 80))
	body = protox.AppendString(body, 2, "replace the line with hello routed")
	got := promptFrom(body, "model-12345678")
	if !strings.Contains(got, "hello routed") || strings.Contains(got, "rule text rule") {
		t.Fatalf("%q", got)
	}
}

func TestPromptUsesTheCmdKQueryNotSlashCommands(t *testing.T) {
	query := protox.AppendString(nil, 1, "replace the selection with hello routed")
	selection := protox.AppendString(nil, 1, "old line")
	ctx := protox.AppendBytes(nil, 4, selection)
	ctx = protox.AppendBytes(ctx, 6, query)
	ctx = protox.AppendBytes(ctx, 5, protox.AppendString(nil, 1, "hello.txt"))
	body := protox.AppendBytes(nil, 1, protox.AppendBytes(nil, 1, ctx))
	for i := 0; i < 6; i++ {
		body = protox.AppendString(body, 16, "/summarize compact the chat")
	}
	got := promptFrom(body, "model-12345678")
	if !strings.Contains(got, "hello routed") || !strings.Contains(got, "old line") || !strings.Contains(got, "hello.txt") || strings.Contains(got, "/summarize") {
		t.Fatalf("%q", got)
	}
}

func TestPromptUsesTheTerminalQuery(t *testing.T) {
	query := protox.AppendString(nil, 1, "echo hello-terminal")
	ctx := protox.AppendBytes(nil, 15, query)
	body := protox.AppendBytes(nil, 1, protox.AppendBytes(nil, 1, ctx))
	for i := 0; i < 4; i++ {
		body = protox.AppendString(body, 16, "/summarize compact the chat")
	}
	got := promptFrom(body, "model-12345678")
	if got != "echo hello-terminal" {
		t.Fatalf("%q", got)
	}
}

func TestPromptDropsSlashCommandsWhenThereIsNoQuery(t *testing.T) {
	body := protox.AppendString(nil, 1, "replace the line with hello routed")
	body = protox.AppendString(body, 2, "/summarize compact the chat")
	body = protox.AppendString(body, 2, "/ask question the user")
	got := promptFrom(body, "model-12345678")
	if got != "replace the line with hello routed" {
		t.Fatalf("%q", got)
	}
}

func TestPromptSkipsTheModelID(t *testing.T) {
	body := protox.AppendString(nil, 1, "model-12345678")
	body = protox.AppendString(body, 2, "replace foo with bar")
	got := promptFrom(body, "model-12345678")
	if strings.Contains(got, "model-12345678") || !strings.Contains(got, "replace foo with bar") {
		t.Fatalf("%q", got)
	}
}

func TestTrafficCountsLocalAndOfficial(t *testing.T) {
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, func(string) (config.Model, bool) { return config.Model{}, false }, agent.NewHistory(t.TempDir()))
	s.countLocal()
	s.countOfficial()
	s.markCatalog(3)
	s.setLastError("timeout")
	got := s.Traffic()
	if !got.CatalogOK || got.CatalogN != 3 || got.Local != 1 || got.Official != 1 || got.LastError != "timeout" || got.CatalogAt.IsZero() {
		t.Fatalf("%+v", got)
	}
}

func TestInlineTerminalFrameUsesTheCommand(t *testing.T) {
	raw := inlineTerminalFrame("echo hi")
	outer, err := protox.Fields(raw)
	if err != nil || outer[0].Num != 1 {
		t.Fatalf("%v %v", outer, err)
	}
	mid, err := protox.Fields(outer[0].Raw)
	if err != nil || mid[0].Num != 1 {
		t.Fatalf("%v", mid)
	}
	inner, err := protox.Fields(mid[0].Raw)
	if err != nil || string(inner[0].Raw) != "echo hi" {
		t.Fatalf("%v", inner)
	}
}

func TestInlineEditFrameUsesTheEditStream(t *testing.T) {
	raw := inlineEditFrame("hello")
	outer, err := protox.Fields(raw)
	if err != nil || outer[0].Num != 1 {
		t.Fatalf("%v %v", outer, err)
	}
	mid, err := protox.Fields(outer[0].Raw)
	if err != nil || mid[0].Num != 2 {
		t.Fatalf("%v", mid)
	}
	inner, err := protox.Fields(mid[0].Raw)
	if err != nil || string(inner[0].Raw) != "hello" {
		t.Fatalf("%v", inner)
	}
}

func TestInlineChatFrameNestsTheText(t *testing.T) {
	raw := inlineChatFrame("hello")
	outer, err := protox.Fields(raw)
	if err != nil || len(outer) != 1 || outer[0].Num != 1 {
		t.Fatalf("%v %v", outer, err)
	}
	mid, err := protox.Fields(outer[0].Raw)
	if err != nil || mid[0].Num != 4 {
		t.Fatalf("%v", mid)
	}
	inner, err := protox.Fields(mid[0].Raw)
	if err != nil || string(inner[0].Raw) != "hello" {
		t.Fatalf("%v", inner)
	}
}

func TestCmdKServiceIsAnsweredLocally(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"edited\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	const id = "model-12345678"
	s := New(
		func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: id}} },
		func(got string) (config.Model, bool) {
			if got != id {
				return config.Model{}, false
			}
			return config.Model{Type: "openai-chat", BaseURL: upstream.URL, APIKey: "k", Model: "m"}, true
		},
		agent.NewHistory(t.TempDir()),
	)
	body := protox.AppendString([]byte(id), 2, "replace foo with bar")
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.CmdKService/StreamCmdK", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.serveCursor(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("edited")) {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.Bytes())
	}
}

func TestCmdKDefaultUsesTheSelectedModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"edited\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	const id = "model-12345678"
	s := New(
		func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: id, DisplayName: "Mine"}} },
		func(got string) (config.Model, bool) {
			if got != id {
				return config.Model{}, false
			}
			return config.Model{Type: "openai-chat", BaseURL: upstream.URL, APIKey: "k", Model: "m", DisplayName: "Mine"}, true
		},
		agent.NewHistory(t.TempDir()),
	)
	s.warnCustomModel("/aiserver.v1.AiService/GetDefaultModelNudgeData", []byte(id), "")
	details := protox.AppendString(nil, 1, "default")
	body := protox.AppendBytes(nil, 2, protox.AppendBytes(nil, 3, details))
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.CmdKService/StreamCmdK", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.serveCursor(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("edited")) {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.Bytes())
	}
}

func TestInlineTextUsesTheConfiguredModel(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"edited\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	model := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	body := protox.AppendString(nil, 2, "replace foo with bar")
	got, err := inlineText(context.Background(), model, dialer.Direct(), body, "model-12345678")
	if err != nil || got != "edited" || !strings.Contains(seen, "replace foo with bar") {
		t.Fatalf("%v %q", err, got)
	}
}
