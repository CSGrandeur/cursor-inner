package app

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestSyncGrokOffLeavesAnotherInstancesBotAlone(t *testing.T) {
	dir := t.TempDir()
	store, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(f *config.File) error {
		f.TakeoverGrok = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	old := grokApply
	grokApply = func(proxy string) error {
		calls = append(calls, proxy)
		return nil
	}
	defer func() { grokApply = old }()
	a := &App{store: store}
	a.SyncGrok()
	if len(calls) != 0 {
		t.Fatalf("switch off without ownership restarted Grok: %v", calls)
	}
	a.keepMark("grok.managed")
	a.SyncGrok()
	if len(calls) != 1 || calls[0] != "" {
		t.Fatalf("owned switch off should clear once, got %v", calls)
	}
	if a.ownsMark("grok.managed") {
		t.Fatal("clearing should drop the ownership mark")
	}
}

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
