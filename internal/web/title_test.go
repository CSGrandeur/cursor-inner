package web

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

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
