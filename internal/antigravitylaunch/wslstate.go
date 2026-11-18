package antigravitylaunch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/fsutil"
)

const wslStateTTL = 5 * time.Minute

type wslStateDoc struct {
	Distro string `json:"distro"`
}

// wslStateGuard remembers the last WSL distro seen while a managed Antigravity
// instance is running, and restores wsl_state.json if a brief unmanaged launch
// fails and clears it before cursor-inner can restart Antigravity under proxy.
type wslStateGuard struct {
	mu        sync.Mutex
	distro    string
	seenAt    time.Time
	statePath func() string
	now       func() time.Time
}

var defaultWSLGuard = &wslStateGuard{
	statePath: defaultWSLStatePath,
	now:       time.Now,
}

func defaultWSLStatePath() string {
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		dir = os.Getenv("APPDATA")
	}
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "Antigravity", "wsl_state.json")
}

func (g *wslStateGuard) path() string {
	if g.statePath != nil {
		return g.statePath()
	}
	return defaultWSLStatePath()
}

func (g *wslStateGuard) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// ObserveManaged records the current distro from wsl_state.json while a managed
// Antigravity instance is running. If the user explicitly switches back to local
// mode inside the managed session, distro becomes "" and won't be restored later.
func (g *wslStateGuard) ObserveManaged() {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.path()
	if p == "" {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			g.distro = ""
			g.seenAt = g.clock()
		}
		return
	}
	var doc wslStateDoc
	if json.Unmarshal(raw, &doc) != nil {
		return
	}
	g.distro = strings.TrimSpace(doc.Distro)
	g.seenAt = g.clock()
}

// RestoreIfWiped restores the previously observed WSL distro when an unmanaged
// launch wiped wsl_state.json to {"distro":""} within the recent window.
func (g *wslStateGuard) RestoreIfWiped() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.distro == "" || g.seenAt.IsZero() {
		return "", false
	}
	now := g.clock()
	if now.Sub(g.seenAt) > wslStateTTL {
		return "", false
	}
	p := g.path()
	if p == "" {
		return "", false
	}
	info, err := os.Stat(p)
	if err != nil || info.ModTime().Before(g.seenAt.Add(-2*time.Second)) {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var doc wslStateDoc
	if json.Unmarshal(raw, &doc) != nil || strings.TrimSpace(doc.Distro) != "" {
		return "", false
	}
	encoded, err := json.Marshal(wslStateDoc{Distro: g.distro})
	if err != nil {
		return "", false
	}
	if err := fsutil.WriteFile(p, encoded); err != nil {
		return "", false
	}
	g.seenAt = now
	return g.distro, true
}
