package takeover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCursorProxy(t *testing.T, proxy string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	// settingsPath uses UserConfigDir; on linux that's XDG_CONFIG_HOME
	path := filepath.Join(cfg, "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"http.proxy":"` + proxy + `","http.proxySupport":"override"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyStartupFirstTime(t *testing.T) {
	dir := t.TempDir()
	kind, addr, err := ClassifyStartup(dir)
	if err != nil || kind != StartEnabled || addr != "" {
		t.Fatalf("kind=%q addr=%q err=%v", kind, addr, err)
	}
}

func TestClassifyStartupResumesFromSettingsAndLegacyMark(t *testing.T) {
	writeCursorProxy(t, "http://127.0.0.1:55214")
	dir := t.TempDir()
	if err := Mark(dir); err != nil { // legacy "1"
		t.Fatal(err)
	}
	kind, addr, err := ClassifyStartup(dir)
	if err != nil || kind != StartResumed || addr != "127.0.0.1:55214" {
		t.Fatalf("kind=%q addr=%q err=%v", kind, addr, err)
	}
}

func TestClassifyStartupResumesFromMarkURL(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	// no settings proxy; mark carries the URL
	dir := t.TempDir()
	if err := MarkURL(dir, "http://127.0.0.1:56134"); err != nil {
		t.Fatal(err)
	}
	kind, addr, err := ClassifyStartup(dir)
	if err != nil || kind != StartResumed || addr != "127.0.0.1:56134" {
		t.Fatalf("kind=%q addr=%q err=%v", kind, addr, err)
	}
}

func TestClassifyStartupMarkedWithoutAddrDoesNotEnable(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := t.TempDir()
	if err := Mark(dir); err != nil {
		t.Fatal(err)
	}
	kind, addr, err := ClassifyStartup(dir)
	if err == nil || kind == StartEnabled || addr != "" {
		t.Fatalf("must not fall back to first-time enable: kind=%q addr=%q err=%v", kind, addr, err)
	}
}

func TestClassifyStartupRejectsMismatchedMarkAndSettings(t *testing.T) {
	writeCursorProxy(t, "http://127.0.0.1:55214")
	dir := t.TempDir()
	if err := MarkURL(dir, "http://127.0.0.1:56134"); err != nil {
		t.Fatal(err)
	}
	kind, _, err := ClassifyStartup(dir)
	if err == nil || kind == StartEnabled {
		t.Fatalf("mismatch must not enable: kind=%q err=%v", kind, err)
	}
}

func TestLoopbackHost(t *testing.T) {
	if loopbackHost("http://127.0.0.1:9") != "127.0.0.1:9" {
		t.Fatal("url")
	}
	if loopbackHost("127.0.0.1:9") != "127.0.0.1:9" {
		t.Fatal("hostport")
	}
	if loopbackHost("http://proxy.example.com:8080") != "" {
		t.Fatal("corp proxy must not resume")
	}
}

func TestAdoptSourceDoesNotCallTerminateCursor(t *testing.T) {
	raw, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	adopt := between(text, "func (s *Service) Adopt(", "func currentSettingsProxy")
	if strings.Contains(adopt, "TerminateCursor") {
		t.Fatal("Adopt must not terminate Cursor")
	}
	resume := between(text, "func (s *Service) ResumeOrEnable(", "")
	if strings.Contains(resume, "Enable()") && !strings.Contains(resume, "kind == StartResumed") {
		t.Fatal("unexpected ResumeOrEnable")
	}
	// resumed path calls Adopt only; Enable only on StartEnabled
	if !strings.Contains(resume, "s.Adopt(addr)") || !strings.Contains(resume, "s.Enable()") {
		t.Fatal("ResumeOrEnable should call Adopt or Enable by kind")
	}
}

func TestUpdatePackagesNeverCallTerminateCursor(t *testing.T) {
	roots := []string{
		filepath.Join("..", "selfupdate"),
		filepath.Join("..", "program", "update.go"),
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(root, err)
		}
		var files []string
		if info.IsDir() {
			ents, _ := os.ReadDir(root)
			for _, e := range ents {
				if strings.HasSuffix(e.Name(), ".go") {
					files = append(files, filepath.Join(root, e.Name()))
				}
			}
		} else {
			files = append(files, root)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "TerminateCursor") || strings.Contains(string(b), "terminateCursor(") {
				t.Fatalf("%s must not call TerminateCursor", f)
			}
		}
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i:]
	if end == "" {
		return s
	}
	j := strings.Index(s, end)
	if j < 0 {
		return s
	}
	return s[:j]
}
