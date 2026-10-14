package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 读取接口（尤其是返回明文 API Key 的）也要挡跨站：别的网页带 Sec-Fetch-Site: cross-site 或外站 Origin 时拒绝。
func TestGuardRefusesCrossSiteAPIRead(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := guard(ok)
	cases := []struct {
		name, origin, site string
		want               int
	}{
		{"cross-site fetch", "https://evil.example", "cross-site", http.StatusForbidden},
		{"same-site other port", "http://127.0.0.1:9999", "same-site", http.StatusForbidden},
		{"page itself", "http://127.0.0.1:8080", "same-origin", http.StatusOK},
		{"curl", "", "", http.StatusOK},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/models/m1/key", nil)
		r.Host = "127.0.0.1:8080"
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, w.Code, c.want)
		}
	}
	// 页面本身（GET /）不受影响：浏览器从地址栏打开时 Sec-Fetch-Site 可能是 none 或 cross-site（从别处点链接）。
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("page open from link: got %d", w.Code)
	}
}
