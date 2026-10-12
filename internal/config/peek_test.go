package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDesktopDefaultsKeepOldBehavior(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"takeover":true,"proxy":{"enabled":true}}`), 0o600)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Get().Desktop; d.Background || d.Quiet {
		t.Fatalf("old config must keep the console window and notifications: %+v", d)
	}
	if err := s.Update(func(f *File) error { f.Desktop.Background = true; return nil }); err != nil {
		t.Fatal(err)
	}
	f, ok := Peek(dir)
	if !ok || !f.Desktop.Background || f.Desktop.Quiet {
		t.Fatalf("background must persist: %+v ok=%v", f.Desktop, ok)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	_ = s.Update(func(f *File) error { f.Desktop = Desktop{}; return nil })
	raw2, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !strings.Contains(string(raw), `"desktop"`) || strings.Contains(string(raw2), `"desktop"`) {
		t.Fatalf("desktop should only be written when set:\n%s\n%s", raw, raw2)
	}
}
