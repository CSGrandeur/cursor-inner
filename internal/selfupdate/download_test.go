package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func serveBytes(b []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
}

const abcSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

func TestDownloadVerifies(t *testing.T) {
	srv := serveBytes([]byte("abc"))
	defer srv.Close()
	dir := t.TempDir()
	p, err := Download(context.Background(), okRoute("default"), []string{srv.URL}, Asset{Name: "x.exe", Size: 3, SHA256: abcSHA}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "abc" {
		t.Fatalf("content %q", b)
	}
}

func TestDownloadChecksumFailure(t *testing.T) {
	cases := map[string][]byte{"tampered": []byte("abd"), "short": []byte("ab"), "long": []byte("abcd")}
	for name, body := range cases {
		srv := serveBytes(body)
		dir := t.TempDir()
		_, err := Download(context.Background(), okRoute("default"), []string{srv.URL, srv.URL}, Asset{Name: "x.exe", Size: 3, SHA256: abcSHA}, dir, nil)
		srv.Close()
		if !errors.Is(err, ErrChecksum) {
			t.Errorf("%s: want ErrChecksum, got %v", name, err)
		}
		left, _ := filepath.Glob(filepath.Join(dir, "*"))
		if len(left) != 0 {
			t.Errorf("%s: left files %v", name, left)
		}
	}
}

func TestDownloadTriesNextURL(t *testing.T) {
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()
	good := serveBytes([]byte("abc"))
	defer good.Close()
	if _, err := Download(context.Background(), okRoute("default"), []string{bad.URL, good.URL}, Asset{Name: "x", Size: 3, SHA256: abcSHA}, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
}
