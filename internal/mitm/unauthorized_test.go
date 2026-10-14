package mitm

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

func TestForwardLogsOfficialUnauthorized(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, nil, nil)
	s.client = upstream.Client()
	host := strings.TrimPrefix(upstream.URL, "https://")
	req := httptest.NewRequest(http.MethodPost, "https://"+host+"/aiserver.v1.TraceService/SubmitSpans", strings.NewReader("x"))
	req.Header.Set("x-request-id", "req-401")
	rec := httptest.NewRecorder()
	s.forward(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "官方返回未登录") || !strings.Contains(out, "SubmitSpans") || !strings.Contains(out, "req-401") {
		t.Fatalf("log=%q", out)
	}
}
