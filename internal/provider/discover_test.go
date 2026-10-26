package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoveryURLs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"https://api.example.com/v1", []string{"https://api.example.com/v1/models"}},
		{"https://api.example.com", []string{"https://api.example.com/v1/models", "https://api.example.com/models"}},
		{"https://api.example.com/v1/chat/completions", []string{"https://api.example.com/v1/models"}},
		{"https://api.example.com/v1/responses", []string{"https://api.example.com/v1/models"}},
		{"https://api.example.com/v1/messages", []string{"https://api.example.com/v1/models"}},
		{"https://api.example.com/coding", []string{"https://api.example.com/coding/v1/models", "https://api.example.com/coding/models"}},
		{"https://api.example.com/api/paas/v4", []string{"https://api.example.com/api/paas/v4/models"}},
		{"https://api.example.com/custom/models", []string{"https://api.example.com/custom/models"}},
	}
	for _, tc := range cases {
		got, err := discoveryURLs(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestDiscoverModelsOpenAI(t *testing.T) {
	var sawAuth string
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "b-model"},
				map[string]any{"id": "a-model"},
				map[string]any{"id": "a-model"},
				"bare-string",
			},
		})
	}))
	t.Cleanup(srv.Close)

	models, err := DiscoverModels(context.Background(), DiscoverInput{
		Type:    "openai-chat",
		BaseURL: srv.URL + "/v1",
		APIKey:  "testkey",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer testkey" {
		t.Fatalf("auth %q", sawAuth)
	}
	if sawPath != "/v1/models" {
		t.Fatalf("path %q", sawPath)
	}
	want := []string{"a-model", "b-model", "bare-string"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("got %v want %v", models, want)
	}
	raw, _ := json.Marshal(models)
	if strings.Contains(string(raw), "sk-test") {
		t.Fatal("api key leaked into response")
	}
}

func TestDiscoverModelsFallsBackToModels(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{"id": "fallback-m"}},
		})
	}))
	t.Cleanup(srv.Close)

	models, err := DiscoverModels(context.Background(), DiscoverInput{
		Type:    "openai-responses",
		BaseURL: srv.URL,
		APIKey:  "k",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits < 2 {
		t.Fatalf("hits %d", hits)
	}
	if len(models) != 1 || models[0] != "fallback-m" {
		t.Fatalf("%v", models)
	}
}

func TestDiscoverModelsAnthropicPages(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "ak" || r.Header.Get("anthropic-version") == "" {
			http.Error(w, "auth", 401)
			return
		}
		page++
		if page == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":     []any{map[string]any{"id": "claude-a"}},
				"has_more": true,
				"last_id":  "claude-a",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":     []any{map[string]any{"id": "claude-b"}},
			"has_more": false,
		})
	}))
	t.Cleanup(srv.Close)

	models, err := DiscoverModels(context.Background(), DiscoverInput{
		Type:    "anthropic",
		BaseURL: srv.URL + "/v1",
		APIKey:  "ak",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"claude-a", "claude-b"}) {
		t.Fatalf("%v", models)
	}
}

func TestDiscoverModelsRequiresFields(t *testing.T) {
	_, err := DiscoverModels(context.Background(), DiscoverInput{Type: "openai-chat", APIKey: "k"}, nil)
	if err == nil {
		t.Fatal("expected base url error")
	}
	_, err = DiscoverModels(context.Background(), DiscoverInput{Type: "openai-chat", BaseURL: "https://x/v1"}, nil)
	if err == nil {
		t.Fatal("expected key error")
	}
}

func TestDiscoverModelsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := DiscoverModels(context.Background(), DiscoverInput{
		Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err=%v", err)
	}
}
