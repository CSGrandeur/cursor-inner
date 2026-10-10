package app

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestGrokProxyBridgesCredentialsAndNeverGoesDirect(t *testing.T) {
	a := &App{}
	defer a.bridge.Close()
	u, keep := a.grokProxy(config.File{Proxy: config.Proxy{Enabled: true, Address: "http://user:pw@127.0.0.1:1080"}})
	if keep || !strings.HasPrefix(u, "http://127.0.0.1:") || strings.Contains(u, "user") || strings.HasSuffix(u, ":1080") {
		t.Fatalf("credentials must go through the loopback bridge, got %q keep=%v", u, keep)
	}
	u, keep = a.grokProxy(config.File{Proxy: config.Proxy{Enabled: true, Address: "127.0.0.1:1080"}})
	if keep || u != "http://127.0.0.1:1080" {
		t.Fatalf("plain http proxy is handed over as is, got %q", u)
	}
	if u, keep = a.grokProxy(config.File{Proxy: config.Proxy{Enabled: true, Address: "::bad::"}}); !keep || u != "" {
		t.Fatalf("an invalid address must keep Grok as is, got %q keep=%v", u, keep)
	}
	if u, keep = a.grokProxy(config.File{Proxy: config.Proxy{Enabled: false, Address: "127.0.0.1:1080"}}); keep || u != "" {
		t.Fatalf("proxy switched off by the user: %q keep=%v", u, keep)
	}
}
