package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/antiaccount"
	"cursor-inner/internal/cursoraccount"
	"cursor-inner/internal/cursorlaunch"
	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/winproc"
)

var (
	foregroundExeFn  = winproc.ForegroundExe
	seedRecentAppsFn = defaultSeedRecentApps
)

func defaultSeedRecentApps() (cursorNS, antiNS int64) {
	return cursoraccount.LatestActivityNano(cursorlaunch.LastRememberedExe()),
		antiaccount.LatestActivityNano("")
}

type recentAppsFile struct {
	CursorNS      int64 `json:"cursor_ns,omitempty"`
	AntigravityNS int64 `json:"antigravity_ns,omitempty"`
}

const recentFlushInterval = 10 * time.Second

func (a *App) ensureRecent() {
	a.recentOnce.Do(func() {
		a.recentMu.Lock()
		defer a.recentMu.Unlock()
		if a.store != nil && a.store.Dir() != "" {
			a.recentPath = filepath.Join(a.store.Dir(), "recent-apps.json")
			if raw, err := os.ReadFile(a.recentPath); err == nil {
				var doc recentAppsFile
				if json.Unmarshal(raw, &doc) == nil {
					a.recentCursorNS = doc.CursorNS
					a.recentAntiNS = doc.AntigravityNS
				}
			}
		}
		if (a.recentCursorNS == 0 || a.recentAntiNS == 0) && seedRecentAppsFn != nil {
			cNS, aNS := seedRecentAppsFn()
			if a.recentCursorNS == 0 {
				a.recentCursorNS = cNS
			}
			if a.recentAntiNS == 0 {
				a.recentAntiNS = aNS
			}
		}
	})
}

// NoteAppUsed 记录用户刚使用或切到某款 IDE，动态更新账号标签排序（最新在最前）。
func (a *App) NoteAppUsed(name string) {
	a.ensureRecent()
	now := time.Now().UnixNano()
	a.recentMu.Lock()
	defer a.recentMu.Unlock()

	prevWinner := orderLeader(a.recentCursorNS, a.recentAntiNS)
	switch name {
	case "cursor":
		if now <= a.recentCursorNS {
			now = a.recentCursorNS + 1
		}
		a.recentCursorNS = now
	case "antigravity":
		if now <= a.recentAntiNS {
			now = a.recentAntiNS + 1
		}
		a.recentAntiNS = now
	default:
		return
	}
	newWinner := orderLeader(a.recentCursorNS, a.recentAntiNS)
	leaderChanged := prevWinner != newWinner
	if a.recentPath != "" && (leaderChanged || a.recentLastFlush.IsZero() || time.Since(a.recentLastFlush) >= recentFlushInterval) {
		a.persistRecentLocked()
	}
}

func (a *App) persistRecentLocked() {
	if a.recentPath == "" {
		return
	}
	raw, err := json.MarshalIndent(recentAppsFile{
		CursorNS:      a.recentCursorNS,
		AntigravityNS: a.recentAntiNS,
	}, "", "  ")
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	if fsutil.WriteFile(a.recentPath, raw) == nil {
		_ = os.Chmod(a.recentPath, 0o600)
		a.recentLastFlush = time.Now()
	}
}

func orderLeader(cursorNS, antiNS int64) string {
	if antiNS > cursorNS {
		return "antigravity"
	}
	return "cursor"
}

// AccountOrder 按最近使用时间降序返回账号板块子标签顺序。
func (a *App) AccountOrder() []string {
	a.ensureRecent()
	a.recentMu.Lock()
	c, ag := a.recentCursorNS, a.recentAntiNS
	a.recentMu.Unlock()
	if ag > c {
		return []string{"antigravity", "cursor"}
	}
	return []string{"cursor", "antigravity"}
}

// TickRecentApps 由每秒循环调用，零子进程感知前台活动窗口归属 Cursor 或 Antigravity。
func (a *App) TickRecentApps() {
	if foregroundExeFn == nil {
		return
	}
	fg := strings.TrimSpace(foregroundExeFn())
	if fg == "" {
		return
	}
	base := strings.ToLower(filepath.Base(fg))
	switch base {
	case "cursor.exe":
		cursorlaunch.RememberExe(fg)
		a.NoteAppUsed("cursor")
	case "antigravity.exe":
		a.NoteAppUsed("antigravity")
	}
}

type recentAppsFields struct {
	recentOnce      sync.Once
	recentMu        sync.Mutex
	recentPath      string
	recentCursorNS  int64
	recentAntiNS    int64
	recentLastFlush time.Time
}
