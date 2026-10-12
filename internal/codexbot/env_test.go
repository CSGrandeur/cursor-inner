package codexbot

import (
	"strings"
	"testing"
)

func TestEnvSetsWebsocketProxyAndDropsInheritedNoProxy(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"NO_PROXY=*",
		"HTTPS_PROXY=http://old:1",
		"WS_PROXY=http://old:1",
	}
	got := Env("http://127.0.0.1:1080", base)
	m := map[string]string{}
	for _, entry := range got {
		name, value, _ := strings.Cut(entry, "=")
		m[name] = value
	}
	if m["PATH"] != "/bin" {
		t.Fatal(m["PATH"])
	}
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "WS_PROXY", "WSS_PROXY"} {
		if m[key] != "http://127.0.0.1:1080" {
			t.Fatalf("%s=%q", key, m[key])
		}
	}
	if strings.Contains(m["NO_PROXY"], "*") || !strings.Contains(m["NO_PROXY"], "127.0.0.1") {
		t.Fatal(m["NO_PROXY"])
	}
	if Args("") != nil {
		t.Fatal("empty proxy has no args")
	}
	joined := strings.Join(Args("http://127.0.0.1:1080"), " ")
	if !strings.Contains(joined, "--proxy-server=http://127.0.0.1:1080") || !strings.Contains(joined, "--disable-quic") {
		t.Fatal(joined)
	}
}
