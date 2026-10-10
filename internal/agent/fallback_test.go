package agent

import (
	"errors"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/provider"
)

func TestFallbackableKinds(t *testing.T) {
	if !Fallbackable(&provider.APIError{Kind: provider.KindAuth}) {
		t.Fatal("auth")
	}
	if !Fallbackable(&provider.APIError{Kind: provider.KindServer, Status: 503}) {
		t.Fatal("5xx")
	}
	if !Fallbackable(&provider.APIError{Kind: provider.KindRateLimit}) {
		t.Fatal("429")
	}
	if !Fallbackable(&provider.APIError{Kind: provider.KindContextOverflow}) {
		t.Fatal("overflow")
	}
	if Fallbackable(&provider.APIError{Kind: provider.KindBadRequest}) {
		t.Fatal("400 should not fallback")
	}
	if Fallbackable(errors.New("x")) {
		t.Fatal("plain")
	}
}

func TestResolveFallback(t *testing.T) {
	a := config.Model{ID: "a", DisplayName: "A", Fallback: []string{"b", "missing", "a", "c"}}
	b := config.Model{ID: "b", DisplayName: "B"}
	c := config.Model{ID: "c", DisplayName: "C"}
	got := resolveFallback(a, []config.Model{a, b, c})
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("%+v", got)
	}
}
