package program

import (
	"sync"
	"time"

	"cursor-inner/internal/app"
	"cursor-inner/internal/selfupdate"
)

// leakShown 是“自定义模型被带给官方”在提示文字里保留的时长（图标保持警告色）。
const leakShown = 10 * time.Minute

// trayHub 把程序里的状态汇总给托盘：刷新时读 input，定时 poll 看告警、泄漏计数，订阅更新快照看失败。
// 弹不弹由 trayNotifier 决定，配置里关了通知（quiet）就只变图标和提示文字。
type trayHub struct {
	version string
	debug   bool
	zh      bool

	status  func() selfupdate.Status
	alerts  func() []app.Alert
	traffic func() (leaks int, name string)
	quiet   func() bool
	send    func(balloon)

	mu       sync.Mutex
	n        *trayNotifier
	current  []string
	leakName string
	leakAt   time.Time
}

func newTrayHub(version string, debug, zh bool) *trayHub {
	return &trayHub{version: version, debug: debug, zh: zh, n: newTrayNotifier(zh, debug)}
}

// input 是托盘刷新时用的快照。
func (h *trayHub) input(now time.Time) trayInput {
	in := trayInput{Version: h.version, Debug: h.debug, Zh: h.zh}
	if h.status != nil {
		in.Update = h.status()
	}
	h.mu.Lock()
	in.Alerts = append([]string(nil), h.current...)
	if !h.leakAt.IsZero() && now.Sub(h.leakAt) < leakShown {
		in.Alerts = append(in.Alerts, pick(h.zh, "自定义模型“"+h.leakName+"”被带给了官方", "Custom model \""+h.leakName+"\" was sent to the official service"))
	}
	h.mu.Unlock()
	return in
}

// poll 每隔几秒调用一次：更新告警列表，必要时弹通知。
func (h *trayHub) poll(now time.Time) {
	var out []*balloon
	var texts []string
	var leakAlerts []string
	if h.alerts != nil {
		for _, a := range h.alerts() {
			text := pick(h.zh, a.Text.Zh, a.Text.En)
			if text == "" {
				continue
			}
			texts = append(texts, text)
			if a.Leak {
				leakAlerts = append(leakAlerts, text)
			}
		}
	}
	h.mu.Lock()
	h.current = texts
	for _, text := range leakAlerts {
		out = append(out, h.n.alert(text, now))
	}
	if h.traffic != nil {
		count, name := h.traffic()
		seen, before := h.n.leakSeen, h.n.leaks
		out = append(out, h.n.leak(count, name, now))
		if seen && count > before {
			h.leakName, h.leakAt = name, now
		}
	}
	h.mu.Unlock()
	h.emit(out...)
}

// onUpdate 看订阅到的每一条更新快照（不会漏掉一闪而过的状态）。
func (h *trayHub) onUpdate(st selfupdate.Status) {
	h.mu.Lock()
	b := h.n.update(st)
	h.mu.Unlock()
	h.emit(b)
}

func (h *trayHub) emit(bs ...*balloon) {
	for _, b := range bs {
		if b == nil || h.send == nil || (h.quiet != nil && h.quiet()) {
			continue
		}
		h.send(*b)
	}
}
