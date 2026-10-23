package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const signedQuery = "sp=r&sv=2018-11-09&se=2026-10-20T16%3A55Z&sig=SECRETSIG&response-content-disposition=attachment"

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://release-assets.githubusercontent.com/github-production-release-asset/1/abc?" + signedQuery + "#frag": "https://release-assets.githubusercontent.com/github-production-release-asset/1/abc",
		"https://user:pass@example.com/p?q=1": "https://example.com/p",
		"not a url":                           "(url)",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// GitHub 把资源地址 302 到带签名查询串的临时地址；那一跳失败时，错误里不能带出查询串，
// 而且仍然归到网络类，界面给网络的说明。
func TestRedirectFailureHidesSignedQuery(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close() // 连接被掐断：客户端拿到带完整地址的 *url.Error
	}))
	defer dead.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dead.URL+"/asset?"+signedQuery, http.StatusFound)
	}))
	defer front.Close()
	route := Route{Name: "default", Client: &http.Client{}}

	_, _, err := fetch(context.Background(), route, front.URL+"/manifest.json", 5*time.Second, 1<<20)
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
		msg := err.Error()
		if strings.Contains(msg, "SECRETSIG") || strings.Contains(msg, "?") || strings.Contains(msg, "sp=r") {
			t.Fatalf("%s: error leaks the signed query: %s", name, msg)
		}
		if !strings.Contains(msg, "/asset") {
			t.Fatalf("%s: error should keep host and path for the log: %s", name, msg)
		}
		var ue *url.Error
		if !errors.As(err, &ue) || ErrorCode(err) != CodeNetwork {
			t.Fatalf("%s: want a network-class *url.Error, got code %q", name, ErrorCode(err))
		}
		for _, zh := range []bool{true, false} {
			if m := UserMessage(ErrorCode(err), zh); strings.Contains(m, "http") || strings.Contains(m, "SECRET") {
				t.Fatalf("%s: user message carries dev details: %s", name, m)
			}
		}
	}
	check("fetch", err)

	j := &job{url: front.URL + "/asset", name: "asset"}
	check("fetchLeg", j.fetchLeg(context.Background(), &leg{route: route}))
}
