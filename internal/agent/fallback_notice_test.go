package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

func TestFallbackShowsSwitchInConversation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Model == "primary" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	mk := func(name string) config.Model {
		m, err := provider.Prepare(config.Model{DisplayName: name, Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: name})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	backup := mk("backup")
	primary := mk("primary")
	primary.Fallback = []string{backup.ID}
	var shown strings.Builder
	used, reply, err := chatWithFallback(context.Background(), primary, []config.Model{primary, backup},
		func(config.Model) (dialer.Func, error) { return dialer.Direct(), nil },
		"sys", []provider.Message{{Role: "user", Content: "hi"}}, nil,
		func(s string) error { shown.WriteString(s); return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if used.ID != backup.ID || reply.Content != "hello" {
		t.Fatalf("used %s reply %q", used.DisplayName, reply.Content)
	}
	out := shown.String()
	if !strings.Contains(out, "backup") || !strings.Contains(out, "primary") || !strings.HasSuffix(out, "hello") {
		t.Fatalf("conversation did not show the switch: %q", out)
	}
	if strings.Contains(reply.Content, "backup") {
		t.Fatal("the notice must not enter the model history")
	}
}
