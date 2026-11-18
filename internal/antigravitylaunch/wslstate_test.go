package antigravitylaunch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readWSLDistroFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc wslStateDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return doc.Distro
}

func TestWSLStateGuardRestoresRecentlyWipedDistro(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wsl_state.json")
	now := time.Date(2026, 11, 18, 11, 27, 0, 0, time.UTC)
	g := &wslStateGuard{
		statePath: func() string { return p },
		now:       func() time.Time { return now },
	}
	if err := os.WriteFile(p, []byte(`{"distro":"Ubuntu-26.04"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)
	g.ObserveManaged()

	now = now.Add(30 * time.Second)
	if err := os.WriteFile(p, []byte(`{"distro":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)

	distro, ok := g.RestoreIfWiped()
	if !ok || distro != "Ubuntu-26.04" {
		t.Fatalf("RestoreIfWiped = (%q, %v), want (Ubuntu-26.04, true)", distro, ok)
	}
	if got := readWSLDistroFile(t, p); got != "Ubuntu-26.04" {
		t.Fatalf("restored file distro = %q, want Ubuntu-26.04", got)
	}
}

func TestWSLStateGuardRespectsManagedSwitchToLocal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wsl_state.json")
	now := time.Date(2026, 11, 18, 11, 27, 0, 0, time.UTC)
	g := &wslStateGuard{
		statePath: func() string { return p },
		now:       func() time.Time { return now },
	}
	if err := os.WriteFile(p, []byte(`{"distro":"Ubuntu-26.04"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)
	g.ObserveManaged()

	// 用户在受管进程里主动切回本地模式。
	now = now.Add(10 * time.Second)
	if err := os.WriteFile(p, []byte(`{"distro":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)
	g.ObserveManaged()

	now = now.Add(20 * time.Second)
	if distro, ok := g.RestoreIfWiped(); ok {
		t.Fatalf("must not restore after managed switch to local, got (%q, true)", distro)
	}
	if got := readWSLDistroFile(t, p); got != "" {
		t.Fatalf("file distro = %q, want empty", got)
	}
}

func TestWSLStateGuardExpiresAfterTTL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wsl_state.json")
	now := time.Date(2026, 11, 18, 11, 27, 0, 0, time.UTC)
	g := &wslStateGuard{
		statePath: func() string { return p },
		now:       func() time.Time { return now },
	}
	if err := os.WriteFile(p, []byte(`{"distro":"Ubuntu-26.04"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)
	g.ObserveManaged()

	now = now.Add(wslStateTTL + time.Second)
	if err := os.WriteFile(p, []byte(`{"distro":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)

	if distro, ok := g.RestoreIfWiped(); ok {
		t.Fatalf("must not restore after TTL expiry, got (%q, true)", distro)
	}
}

func TestWSLStateGuardPreservesOtherNonEmptyDistro(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wsl_state.json")
	now := time.Date(2026, 11, 18, 11, 27, 0, 0, time.UTC)
	g := &wslStateGuard{
		statePath: func() string { return p },
		now:       func() time.Time { return now },
	}
	if err := os.WriteFile(p, []byte(`{"distro":"Ubuntu-26.04"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)
	g.ObserveManaged()

	now = now.Add(20 * time.Second)
	if err := os.WriteFile(p, []byte(`{"distro":"Debian"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, now, now)

	if distro, ok := g.RestoreIfWiped(); ok {
		t.Fatalf("must not overwrite non-empty distro, got (%q, true)", distro)
	}
	if got := readWSLDistroFile(t, p); got != "Debian" {
		t.Fatalf("file distro = %q, want Debian", got)
	}
}
