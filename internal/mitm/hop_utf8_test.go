package mitm

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRemoveHopClearsConnectionListedHeaders(t *testing.T) {
	h := http.Header{}
	h.Add("Connection", "X-Custom-One, x-custom-two")
	h.Add("Connection", "Keep-Alive")
	h.Set("X-Custom-One", "1")
	h.Set("X-Custom-Two", "2")
	h.Set("X-Keep-Me", "ok")
	removeHop(h)
	if h.Get("Connection") != "" || h.Get("X-Custom-One") != "" || h.Get("X-Custom-Two") != "" {
		t.Fatalf("hop headers remained: %v", h)
	}
	if h.Get("X-Keep-Me") != "ok" {
		t.Fatalf("non-hop header dropped: %v", h)
	}
}

func TestSetLastErrorPreservesValidUTF8(t *testing.T) {
	var s Server
	s.setLastError(strings.Repeat("中文错误信息测试", 20))
	got := s.Traffic().LastError
	if !utf8.ValidString(got) {
		t.Fatalf("setLastError produced invalid UTF-8: %q", got)
	}
	if len([]rune(got)) != 80 {
		t.Fatalf("want 80 runes, got %d", len([]rune(got)))
	}
}
