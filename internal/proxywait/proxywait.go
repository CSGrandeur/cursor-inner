// Package proxywait 在开机自启早于代理软件时，等配置的代理开始监听再做依赖代理的事
// （Grok/Codex 代理桥接管、自动检查更新）。等待期间是“等待代理…”，不是故障；
// 超过 Timeout 仍没监听才算异常，并告诉用户去打开代理软件。探测一直继续，代理一开就自动接上。
//
// 等待期间不放行任何直连：这里只决定“什么时候去做”，官方流量照旧只走代理（代理不通就失败）。
package proxywait

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"
)

type State int

const (
	Idle    State = iota // 没开代理或还没开始
	Waiting              // 代理还没监听，未超时（中性，不报错）
	Ready                // 代理已监听
	Down                 // 超时仍没监听（异常，提示用户打开代理软件）
)

func (s State) String() string {
	switch s {
	case Waiting:
		return "waiting"
	case Ready:
		return "ready"
	case Down:
		return "down"
	}
	return ""
}

// Schedule 是探测间隔：前 Fast 时长内每 FastEvery 一次，之后每 SlowEvery 一次。
type Schedule struct {
	Fast      time.Duration
	FastEvery time.Duration
	SlowEvery time.Duration
	Timeout   time.Duration // 超过就进入 Down
	// 就绪后的看护：每 Health 探一次（0 表示不看护）；拨号失败时 Kick 立即探。
	// 探不通后隔 Debounce 再探一次，仍不通才回到 Waiting，短暂抖动不改界面。
	Health   time.Duration
	Debounce time.Duration
}

var Default = Schedule{Fast: 2 * time.Minute, FastEvery: 2 * time.Second, SlowEvery: 15 * time.Second, Timeout: 2 * time.Minute, Health: 10 * time.Second, Debounce: 3 * time.Second}

// Probe 默认探测：能建 TCP 连接就算代理在监听。
func Probe(addr string) func(context.Context) error {
	return func(ctx context.Context) error {
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return c.Close()
	}
}

type Waiter struct {
	sched Schedule
	now   func() time.Time

	mu      sync.Mutex
	state   State
	addr    string
	since   time.Time
	gen     int
	cancel  context.CancelFunc
	onReady []func()
	kick    chan struct{}
}

func New(s Schedule) *Waiter { return &Waiter{sched: s, now: time.Now, kick: make(chan struct{}, 1)} }

// Kick 报告一次经代理的拨号失败：就绪时尽快复查代理是否还在监听。不阻塞。
func (w *Waiter) Kick() {
	if w == nil {
		return
	}
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// OnReady 注册代理就绪（每次从未就绪变成就绪）时要做的事。
func (w *Waiter) OnReady(fn func()) {
	w.mu.Lock()
	w.onReady = append(w.onReady, fn)
	w.mu.Unlock()
}

func (w *Waiter) State() (State, string, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state, w.addr, w.since
}

// Ready 为真表示可以做依赖代理的事：代理已监听，或根本没在等（没开代理）。
func (w *Waiter) Ready() bool {
	if w == nil {
		return true
	}
	s, _, _ := w.State()
	return s == Ready || s == Idle
}

// Stop 不再等（关掉代理时）。
func (w *Waiter) Stop() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.gen++
	w.state, w.addr = Idle, ""
	w.mu.Unlock()
}

// Start 开始等 addr（host:port）。同一地址已在等或已就绪时不重来。
func (w *Waiter) Start(addr string, probe func(context.Context) error) {
	w.mu.Lock()
	if w.addr == addr && w.state != Idle {
		w.mu.Unlock()
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.gen++
	gen := w.gen
	w.addr, w.since = addr, w.now()
	w.mu.Unlock()

	// 先同步探一次：代理已开时不进入“等待”，不打扰。
	if probe(ctx) == nil {
		w.setReady(ctx, gen, false, probe)
		return
	}
	w.mu.Lock()
	if w.gen == gen {
		w.state = Waiting
	}
	w.mu.Unlock()
	slog.Info("等待代理 "+addr+" 开始监听（代理软件可能还没启动），就绪后自动接上", "addr", addr)
	go w.loop(ctx, gen, addr, probe)
}

// watch 在就绪后看护代理：定时探测，或被 Kick 唤醒；确认断开后回到 Waiting 并重新等。
func (w *Waiter) watch(ctx context.Context, gen int, addr string, probe func(context.Context) error) {
	if w.sched.Health <= 0 {
		return
	}
	// 清掉就绪前积攒的 Kick
	select {
	case <-w.kick:
	default:
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.sched.Health):
		case <-w.kick:
		}
		if probe(ctx) == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.sched.Debounce):
		}
		if probe(ctx) == nil {
			continue
		}
		w.mu.Lock()
		lost := w.gen == gen && w.state == Ready
		if lost {
			w.state, w.since = Waiting, w.now()
		}
		w.mu.Unlock()
		if !lost {
			return
		}
		slog.Info("代理 "+addr+" 连不上了（代理软件可能在重启或已退出），恢复后自动接上；期间官方流量不会直连", "addr", addr)
		w.loop(ctx, gen, addr, probe)
		return
	}
}

func (w *Waiter) loop(ctx context.Context, gen int, addr string, probe func(context.Context) error) {
	start := w.now()
	for {
		elapsed := w.now().Sub(start)
		every := w.sched.SlowEvery
		if elapsed < w.sched.Fast {
			every = w.sched.FastEvery
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if probe(ctx) == nil {
			w.setReady(ctx, gen, true, probe)
			return
		}
		if w.now().Sub(start) >= w.sched.Timeout {
			w.mu.Lock()
			flip := w.gen == gen && w.state == Waiting
			if flip {
				w.state = Down
			}
			w.mu.Unlock()
			if flip {
				slog.Warn("代理 "+addr+" 一直没有开始监听。请打开你的代理软件（例如 v2rayN、Clash）；打开后 cursor-inner 会自动接上，不用重启。等待期间官方流量不会直连。", "addr", addr)
			}
		}
	}
}

func (w *Waiter) setReady(ctx context.Context, gen int, waited bool, probe func(context.Context) error) {
	w.mu.Lock()
	if w.gen != gen {
		w.mu.Unlock()
		return
	}
	w.state = Ready
	since := w.since
	addr := w.addr
	fns := append([]func(){}, w.onReady...)
	w.mu.Unlock()
	if waited {
		slog.Info("代理 "+addr+" 已就绪，开始接管", "waited", w.now().Sub(since).Round(time.Second).String())
	}
	for _, fn := range fns {
		fn()
	}
	go w.watch(ctx, gen, addr, probe)
}
