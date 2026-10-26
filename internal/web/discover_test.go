package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/i18n"
)

func TestDiscoverModelsEndpoint(t *testing.T) {
	f := &fake{discover: []string{"m-b", "m-a"}, discoverErr: nil}
	srv := httptest.NewServer(Handler(f))
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/api/models/discover", "application/json", strings.NewReader(
		`{"type":"openai-chat","base_url":"https://api.example.com/v1","api_key":"sk-secret","use_proxy":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 2 || body.Models[0] != "m-b" {
		t.Fatalf("%+v", body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "sk-secret") {
		t.Fatal("key echoed")
	}
	if f.discoverType != "openai-chat" || f.discoverURL != "https://api.example.com/v1" || f.discoverKey != "sk-secret" {
		t.Fatalf("forward %+v %+v %+v", f.discoverType, f.discoverURL, f.discoverKey)
	}
}

func TestDiscoverModelsEndpointError(t *testing.T) {
	f := &fake{discoverErr: i18n.E("boom", "boom")}
	srv := httptest.NewServer(Handler(f))
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+"/api/models/discover", "application/json", strings.NewReader(
		`{"type":"openai-chat","base_url":"https://x/v1","api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestDiscoverModelsGoneV1List(t *testing.T) {
	srv := httptest.NewServer(Handler(&fake{}))
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("legacy /v1/models should be gone, got %d", res.StatusCode)
	}
}
