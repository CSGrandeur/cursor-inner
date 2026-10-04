//go:build linux

package takeover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cursor-inner/internal/cursorsettings"
)

func TestTakeoverRoundTripKeepsUserSettings(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := t.TempDir()
	settings := filepath.Join(cfg, "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"editor.fontSize": 15, "http.proxy": "http://proxy.corp:8080", "http.noProxy": ["corp.local"]}`
	if err := os.WriteFile(settings, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := saveOriginal(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeSettings(cursorsettings.Apply, "http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if err := saveOriginal(dir); err != nil {
		t.Fatal(err)
	}
	if err := restoreSettings(dir); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(settings)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err, string(raw))
	}
	if doc["http.proxy"] != "http://proxy.corp:8080" || doc["editor.fontSize"].(float64) != 15 {
		t.Fatalf("%s", raw)
	}
	if np, _ := doc["http.noProxy"].([]any); len(np) != 1 || np[0] != "corp.local" {
		t.Fatalf("%s", raw)
	}
	if _, ok := doc["http.proxySupport"]; ok {
		t.Fatalf("%s", raw)
	}
	if _, err := os.Stat(backupPath(dir)); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after restore")
	}
}
