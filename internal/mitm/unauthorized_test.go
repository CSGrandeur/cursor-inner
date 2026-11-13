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
	"cursor-inner/internal/logx"
)

func TestForwardLogsOfficialUnauthorized(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	var console, file bytes.Buffer
	prev := slog.Default()
	logx.Setup(&console, &file, false)
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
	if console.Len() != 0 {
		t.Fatalf("console must stay empty for 401 log: %q", console.String())
	}
	out := file.String()
	if !strings.Contains(out, "官方返回未登录") || !strings.Contains(out, "SubmitSpans") || !strings.Contains(out, "req-401") {
		t.Fatalf("file=%q", out)
	}
}
