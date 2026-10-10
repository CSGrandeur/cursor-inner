package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuardRejectsRebindingAndCrossSite(t *testing.T) {
	f := &fake{}
	h := Handler(f)
	cases := []struct {
		name, method, host, origin, site string
		want                             int
	}{
		{"same origin page", http.MethodPost, "127.0.0.1:5000", "http://127.0.0.1:5000", "same-origin", http.StatusOK},
		{"local client without headers", http.MethodPost, "127.0.0.1:5000", "", "", http.StatusOK},
		{"localhost", http.MethodGet, "localhost:5000", "", "", http.StatusOK},
		{"dns rebinding read", http.MethodGet, "evil.example:5000", "", "", http.StatusForbidden},
		{"dns rebinding write", http.MethodPost, "evil.example:5000", "http://evil.example:5000", "same-origin", http.StatusForbidden},
		{"cross-site origin", http.MethodPost, "127.0.0.1:5000", "https://evil.example", "cross-site", http.StatusForbidden},
		{"null origin", http.MethodPost, "127.0.0.1:5000", "null", "", http.StatusForbidden},
		{"cross-site fetch metadata only", http.MethodPost, "127.0.0.1:5000", "", "cross-site", http.StatusForbidden},
	}
	for _, c := range cases {
		path := "/api/quit"
		if c.method == http.MethodGet {
			path = "/api/state"
		}
		req := httptest.NewRequest(c.method, "http://"+c.host+path, strings.NewReader(""))
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
		if rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing X-Frame-Options", c.name)
		}
	}
	if !f.quit {
		t.Fatal("allowed quit request did not reach the backend")
	}
}
