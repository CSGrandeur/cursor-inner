package mitm

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

func TestTraceRecordsKnownAndUnknownPaths(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Path", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(append([]byte("echo:"), body...))
	}))
	defer upstream.Close()
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, nil, nil)
	s.client = upstream.Client()
	var got []TraceEvent
	SetTrace(func(ev TraceEvent) {
		got = append(got, TraceEvent{
			Host: ev.Host, Method: ev.Method, Path: ev.Path, RawQuery: ev.RawQuery, Status: ev.Status,
			ReqBody: append([]byte(nil), ev.ReqBody...), RespBody: append([]byte(nil), ev.RespBody...),
		})
	}, "")
	t.Cleanup(func() { SetTrace(nil, "") })

	paths := []string{"/agent.v1.AgentService/RunSSE", "/not.in.any.list/Whatever"}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, upstream.URL+path+"?x=1", strings.NewReader(path))
		rec := httptest.NewRecorder()
		s.serveCursor(rec, req)
		if rec.Code != http.StatusCreated || rec.Body.String() != "echo:"+path {
			t.Fatalf("%s status %d body %q", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Echo-Path") != path {
			t.Fatalf("%s response header %q", path, rec.Header().Get("X-Echo-Path"))
		}
	}
	if len(got) != len(paths) {
		t.Fatalf("records %d", len(got))
	}
	for i, path := range paths {
		ev := got[i]
		if ev.Method != http.MethodPost || ev.Path != path || ev.RawQuery != "x=1" || ev.Status != http.StatusCreated {
			t.Fatalf("meta %+v", ev)
		}
		if !bytes.Equal(ev.ReqBody, []byte(path)) || !bytes.Equal(ev.RespBody, []byte("echo:"+path)) {
			t.Fatalf("body req %q resp %q", ev.ReqBody, ev.RespBody)
		}
	}
}

func TestTraceUnsetDoesNotChangeForward(t *testing.T) {
	SetTrace(nil, "")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, nil, nil)
	s.client = upstream.Client()
	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/nope", strings.NewReader("abc"))
	rec := httptest.NewRecorder()
	s.serveCursor(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
}
