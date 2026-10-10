package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPeekIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Peek(dir); ok {
		t.Fatal("empty dir must not report a config")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("Peek must not create config.json")
	}
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"proxy":{"enabled":true,"address":"127.0.0.1:1080"}}`), 0o600)
	f, ok := Peek(dir)
	if !ok || !f.Proxy.Enabled || f.Proxy.Address != "127.0.0.1:1080" {
		t.Fatalf("got %+v ok=%v", f.Proxy, ok)
	}
}
