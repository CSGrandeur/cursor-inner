package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 开发版曾写过 "bypass" 名单；字段已删除，旧配置必须照常读取、其余设置不丢。
func TestOldConfigWithBypassFieldStillLoads(t *testing.T) {
	dir := t.TempDir()
	raw := `{"takeover_grok":true,"proxy":{"enabled":true,"address":"127.0.0.1:1080"},"bypass":["alpha.example",".alpha.example","gamma.example"],"models":[]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if !got.Proxy.Enabled || got.Proxy.Address != "127.0.0.1:1080" || !got.TakeoverGrok {
		t.Fatalf("settings lost: %+v", got)
	}
}
