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

func TestForwardKeepsLargeBodies(t *testing.T) {
	var got int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.Copy(io.Discard, r.Body)
	}))
	defer upstream.Close()
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, nil, nil)
	s.client = upstream.Client()
	body := bytes.Repeat([]byte("x"), 40<<20)
	host := strings.TrimPrefix(upstream.URL, "https://")
	req := httptest.NewRequest(http.MethodPost, "https://"+host+"/aiserver.v1.RepositoryService/FastUpdateFileV2", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.forward(rec, req)
	if rec.Code != http.StatusOK || got != int64(len(body)) {
		t.Fatalf("status=%d upstream got %d of %d bytes", rec.Code, got, len(body))
	}
}
