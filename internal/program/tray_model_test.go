package program

import (
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/app"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/selfupdate"
)

func TestTrayLevelMapping(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		alerts []string
		want   trayLevel
	}{
		{"idle", selfupdate.StateIdle, nil, trayNormal},
		{"up to date", selfupdate.StateUpToDate, nil, trayNormal},
		{"available is not busy", selfupdate.StateAvailable, nil, trayNormal},
		{"checking is not busy", selfupdate.StateChecking, nil, trayNormal},
		{"alert", selfupdate.StateIdle, []string{"接管失败"}, trayWarn},
		{"failed update with alert", selfupdate.StateFailed, []string{"x"}, trayWarn},
		{"downloading", selfupdate.StateDownloading, nil, trayUpdating},
		{"verifying", selfupdate.StateVerifying, nil, trayUpdating},
		{"preflight", selfupdate.StatePreflight, nil, trayUpdating},
		{"waiting idle", selfupdate.StateWaitingIdle, nil, trayUpdating},
		{"handover beats alert", selfupdate.StateHandover, []string{"x"}, trayUpdating},
		{"done (draining)", selfupdate.StateDone, nil, trayUpdating},
	}
	for _, c := range cases {
		lvl, _ := trayLook(trayInput{Version: "v1", Zh: true, Update: selfupdate.Status{State: c.state, Latest: "v2"}, Alerts: c.alerts})
		if lvl != c.want {
			t.Errorf("%s: level %d, want %d", c.name, lvl, c.want)
		}
	}
}

func TestTrayTipShowsVersionProgressAndDebug(t *testing.T) {
	st := selfupdate.Status{State: selfupdate.StateDownloading, Current: "v0.5.3", Latest: "v0.5.4", Percent: 42, Speed: 1363149, ETA: 9}
	_, tip := trayLook(trayInput{Version: "v0.5.3", Debug: true, Zh: true, Update: st})
	want := "cursor-inner v0.5.3 · 调试实例\n更新到 v0.5.4：下载 42% · 1.3 MB/s · 剩余约 9 秒"
	if tip != want {
		t.Fatalf("tip\n%q\nwant\n%q", tip, want)
	}
	_, tip = trayLook(trayInput{Version: "v0.5.3", Update: st})
	if tip != "cursor-inner v0.5.3\nUpdating to v0.5.4: downloading 42% · 1.3 MB/s · 9 s left" {
		t.Fatalf("en tip %q", tip)
	}
	_, tip = trayLook(trayInput{Version: "v0.5.3", Zh: true, Update: selfupdate.Status{State: selfupdate.StateUpToDate}})
	if tip != "cursor-inner v0.5.3" {
		t.Fatalf("idle tip %q", tip)
	}
	_, tip = trayLook(trayInput{Version: "v1", Zh: true, Update: selfupdate.Status{State: selfupdate.StateFailed, Current: "v1"}})
	if !strings.HasSuffix(tip, "检查更新失败") {
		t.Fatalf("check failure %q", tip)
	}
	_, tip = trayLook(trayInput{Version: "v1", Zh: true, Alerts: []string{"代理地址无效：x", "TUN 未开启"}})
	if !strings.HasSuffix(tip, "⚠ 代理地址无效：x（另有 1 项）") {
		t.Fatalf("alert line %q", tip)
	}
}

// szTip 只有 128 个 UTF-16 单元：超长时截断最后一行，首行版本号保留，不切开代理对。
func TestTrayTipFitsNotifyIconData(t *testing.T) {
	long := strings.Repeat("很长的错误信息😀", 40)
	_, tip := trayLook(trayInput{Version: "v0.5.3", Debug: true, Zh: true,
		Update: selfupdate.Status{State: selfupdate.StateDownloading, Latest: "v0.5.4", Percent: 5},
		Alerts: []string{long}})
	if n := utf16Len(tip); n > trayTipMax {
		t.Fatalf("tip has %d UTF-16 units", n)
	}
	if !strings.HasPrefix(tip, "cursor-inner v0.5.3 · 调试实例\n更新到 v0.5.4") || !strings.HasSuffix(tip, "…") {
		t.Fatalf("%q", tip)
	}
	if strings.ContainsRune(tip, '\uFFFD') {
		t.Fatalf("split surrogate pair: %q", tip)
	}
	if got := truncUTF16("😀😀", 2); got != "…" {
		t.Fatalf("surrogate pair must not be split: %q", got)
	}
}

func TestTrayMenuItems(t *testing.T) {
	texts := func(items []trayItem) string {
		var out []string
		for _, it := range items {
			switch {
			case it.Sep:
				out = append(out, "-")
			case it.Disabled:
				out = append(out, "("+it.Text+")")
			case it.Default:
				out = append(out, "*"+it.Text)
			default:
				out = append(out, it.Text)
			}
		}
		return strings.Join(out, "|")
	}
	if got := texts(trayMenu(true, true, true, false)); got != "*打开配置页|隐藏控制台窗口|检查更新|-|退出" {
		t.Fatalf("%s", got)
	}
	if got := texts(trayMenu(true, false, true, true)); got != "*打开配置页|显示控制台窗口|(正在更新…)|-|退出" {
		t.Fatalf("%s", got)
	}
	if got := texts(trayMenu(false, false, false, false)); got != "*Open settings|Show console window|-|Quit" {
		t.Fatalf("%s", got)
	}
}

func TestTrayDispatch(t *testing.T) {
	var got []string
	checked := make(chan struct{})
	a := trayActions{
		Open:        func(page string) { got = append(got, "open"+page) },
		ShowConsole: func() { got = append(got, "show") },
		HideConsole: func() { got = append(got, "hide") },
		Check:       func() { close(checked) },
		Quit:        func() { got = append(got, "quit") },
	}
	trayDispatch(menuOpen, false, a)
	trayDispatch(menuConsole, true, a)
	trayDispatch(menuConsole, false, a)
	trayDispatch(0, false, a) // 点了菜单外面
	trayDispatch(menuQuit, false, a)
	if strings.Join(got, ",") != "open,hide,show,quit" {
		t.Fatalf("%v", got)
	}
	trayDispatch(menuUpdate, false, a)
	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("check not run")
	}
	trayDispatch(menuOpen, false, trayActions{}) // 没注入的动作不 panic
}

func TestNotifierUpdateFailureOnlyAfterApplying(t *testing.T) {
	n := newTrayNotifier(true, false)
	seq := []selfupdate.Status{
		{State: selfupdate.StateChecking},
		{State: selfupdate.StateFailed, Error: "连不上"}, // 检查失败：不打扰
		{State: selfupdate.StateAvailable, Latest: "v2"},
		{State: selfupdate.StateDownloading, Latest: "v2"},
		{State: selfupdate.StateFailed, Latest: "v2", Error: "sha256 不符\n已删除"},
	}
	var got []*balloon
	for _, st := range seq {
		if b := n.update(st); b != nil {
			got = append(got, b)
		}
	}
	if len(got) != 1 || !got[0].Warn || got[0].Page != pageUpdate || got[0].Text != "没能更新到 v2：sha256 不符 已删除。当前版本继续运行。" {
		t.Fatalf("%+v", got)
	}
	b := updatedBalloon(true, true, "v1", "v2")
	if b.Title != "cursor-inner（调试实例）" || b.Warn || b.Page != pageUpdate || !strings.Contains(b.Text, "已从 v1 更新到 v2") {
		t.Fatalf("%+v", b)
	}
}

func TestNotifierLeaksAndAlertsAreRateLimited(t *testing.T) {
	n := newTrayNotifier(true, false)
	now := time.Unix(1000, 0)
	if n.leak(3, "Mine", now) != nil {
		t.Fatal("leaks counted before start must not pop")
	}
	if n.leak(3, "Mine", now.Add(time.Second)) != nil {
		t.Fatal("unchanged count must not pop")
	}
	if b := n.leak(4, "Mine", now.Add(2*time.Second)); b == nil || b.Page != pageModels || !strings.Contains(b.Text, "“Mine”") {
		t.Fatalf("new leak must pop: %+v", b)
	}
	if n.leak(5, "Mine", now.Add(time.Minute)) != nil {
		t.Fatal("second leak within the repeat window must not pop")
	}
	if n.leak(6, "Mine", now.Add(leakRepeat+3*time.Second)) == nil {
		t.Fatal("leak after the repeat window must pop again")
	}
	if b := n.alert("TUN 回退", now); b == nil || b.Page != pageNetwork || n.alert("TUN 回退", now.Add(time.Minute)) != nil || n.alert("另一条", now) == nil {
		t.Fatal("alerts are per text and rate limited")
	}
}

func TestTrayHubRespectsQuietAndShowsLeak(t *testing.T) {
	h := newTrayHub("v1", false, true)
	quiet := false
	var sent []balloon
	h.quiet = func() bool { return quiet }
	h.send = func(b balloon) { sent = append(sent, b) }
	leaks := 0
	h.traffic = func() (int, string) { return leaks, "Mine" }
	alerts := []app.Alert{{Text: i18n.T("代理地址无效", "Bad proxy")}}
	h.alerts = func() []app.Alert { return alerts }
	h.status = func() selfupdate.Status { return selfupdate.Status{State: selfupdate.StateUpToDate} }

	now := time.Unix(5000, 0)
	h.poll(now)
	lvl, tip := trayLook(h.input(now))
	if lvl != trayWarn || !strings.Contains(tip, "代理地址无效") || len(sent) != 0 {
		t.Fatalf("plain alert: lvl %d tip %q sent %v", lvl, tip, sent)
	}
	leaks = 1
	h.poll(now.Add(2 * time.Second))
	if len(sent) != 1 || !strings.Contains(h.input(now).Alerts[1], "Mine") {
		t.Fatalf("leak: sent %v alerts %v", sent, h.input(now).Alerts)
	}
	if got := h.input(now.Add(leakShown + 3*time.Second)).Alerts; len(got) != 1 {
		t.Fatalf("leak line must expire: %v", got)
	}
	// 关了通知：图标照样变，不弹。
	quiet = true
	alerts = append(alerts, app.Alert{Text: i18n.T("TUN 未开启，官方端点可能泄漏", "TUN off"), Leak: true})
	h.poll(now.Add(4 * time.Second))
	h.onUpdate(selfupdate.Status{State: selfupdate.StateDownloading, Latest: "v2"})
	h.onUpdate(selfupdate.Status{State: selfupdate.StateFailed, Latest: "v2", Error: "x"})
	if len(sent) != 1 {
		t.Fatalf("quiet must not pop: %v", sent)
	}
	if lvl, _ := trayLook(h.input(now)); lvl != trayWarn {
		t.Fatalf("quiet must still warn: %d", lvl)
	}
}

func TestBadgePixels(t *testing.T) {
	const size = 32
	src := make([]byte, size*size*4)
	for i := 0; i < len(src); i += 4 {
		src[i], src[i+1], src[i+2], src[i+3] = 0x14, 0x15, 0x15, 255 // 深色不透明底
	}
	if _, ok := badgeColor(trayNormal); ok {
		t.Fatal("normal state has no badge")
	}
	c, _ := badgeColor(trayWarn)
	out := badgePixels(src, size, c)
	at := func(x, y int) []byte { i := (y*size + x) * 4; return out[i : i+4] }
	if p := at(size-9, size-9); p[2] != c.r || p[1] != c.g || p[0] != c.b || p[3] != 255 {
		t.Fatalf("dot centre %v", p)
	}
	if p := at(2, 2); p[0] != 0x14 || p[3] != 255 {
		t.Fatalf("top-left must be untouched %v", p)
	}
	cut := false
	for x := 0; x < size; x++ {
		if p := at(x, size-9); p[3] < 128 {
			cut = true // 圆点左侧那道透明缝
		}
	}
	if !cut {
		t.Fatal("badge must be separated from the icon by a transparent gap")
	}
	if src[(size*size-1)*4] != 0x14 {
		t.Fatal("source modified")
	}
	if u, _ := badgeColor(trayUpdating); u == c {
		t.Fatal("warning and updating badges must differ")
	}
}
