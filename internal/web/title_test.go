package web

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTitleMark(t *testing.T) {
	SetTitleMark(" local ")
	t.Cleanup(func() { SetTitleMark("") })
	if got := Title("v0.5.3"); got != "cursor-inner local v0.5.3" {
		t.Fatalf("Title with mark = %q", got)
	}
	if got := Title(""); got != "cursor-inner local" {
		t.Fatalf("Title with mark and no version = %q", got)
	}
	SetTitleMark("")
	if got := Title("v0.5.3"); got != "cursor-inner v0.5.3" {
		t.Fatalf("Title after clearing mark = %q", got)
	}
}

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{
		"v0.3.7":               "cursor-inner v0.3.7",
		"v0.3.7-20261010-1200": "cursor-inner v0.3.7-20261010-1200",
		" dev ":                "cursor-inner dev",
		"":                     "cursor-inner",
	} {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPageTitleCarriesVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"v0.3.7", "<title>cursor-inner v0.3.7</title>"},
		{"v0.3.7-<x>", "<title>cursor-inner v0.3.7-&lt;x&gt;</title>"},
		{"", "<title>cursor-inner</title>"},
	} {
		srv := httptest.NewServer(HandlerVersion(nil, tc.version))
		resp, err := srv.Client().Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		srv.Close()
		if !strings.Contains(string(body), tc.want) {
			t.Errorf("version %q: page lacks %s", tc.version, tc.want)
		}
		if strings.Count(string(body), "<title>") != 1 {
			t.Errorf("version %q: want exactly one <title>", tc.version)
		}
	}
}
