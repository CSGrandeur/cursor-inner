package proxywait

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fast = Schedule{Fast: 200 * time.Millisecond, FastEvery: 10 * time.Millisecond, SlowEvery: 30 * time.Millisecond, Timeout: 100 * time.Millisecond}

func waitState(t *testing.T, w *Waiter, want State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if s, _, _ := w.State(); s == want {
			return
		}
		if time.Now().After(deadline) {
			s, _, _ := w.State()
			t.Fatalf("state=%v want %v", s, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWaitsThenTimesOutThenRecovers(t *testing.T) {
	var up atomic.Bool
	probe := func(context.Context) error {
		if up.Load() {
			return nil
		}
		return errors.New("refused")
	}
	var mu sync.Mutex
	ready := 0
	w := New(fast)
	w.OnReady(func() { mu.Lock(); ready++; mu.Unlock() })
	w.Start("127.0.0.1:1080", probe)
	if s, _, _ := w.State(); s != Waiting || w.Ready() {
		t.Fatalf("closed proxy must be Waiting (neutral), got %v", s)
	}
	waitState(t, w, Down)
	up.Store(true)
	waitState(t, w, Ready)
	for i := 0; i < 200; i++ { // 状态先变 Ready，OnReady 随后执行
		mu.Lock()
		n := ready
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if ready != 1 || !w.Ready() {
		t.Fatalf("OnReady must run exactly once when the proxy comes up, got %d", ready)
	}
}

func TestAlreadyListeningIsReadyWithoutWaiting(t *testing.T) {
	called := 0
	w := New(fast)
	w.OnReady(func() { called++ })
	w.Start("x:1", func(context.Context) error { return nil })
	if s, _, _ := w.State(); s != Ready || called != 1 {
		t.Fatalf("state=%v called=%d", s, called)
	}
	// 同一地址再 Start 不重来
	w.Start("x:1", func(context.Context) error { return errors.New("no") })
	if s, _, _ := w.State(); s != Ready {
		t.Fatal("restart on same address must not reset")
	}
	w.Stop()
	if !w.Ready() {
		t.Fatal("stopped (proxy off) means nothing to wait for")
	}
}

func TestBackoffSlowsDownAfterFastPhase(t *testing.T) {
	var n atomic.Int64
	s := Schedule{Fast: 100 * time.Millisecond, FastEvery: 10 * time.Millisecond, SlowEvery: 200 * time.Millisecond, Timeout: time.Hour}
	w := New(s)
	w.Start("a:1", func(context.Context) error { n.Add(1); return errors.New("no") })
	defer w.Stop()
	time.Sleep(150 * time.Millisecond)
	afterFast := n.Load()
	time.Sleep(300 * time.Millisecond)
	slow := n.Load() - afterFast
	if afterFast < 5 || slow > 3 {
		t.Fatalf("fast phase probes=%d, slow phase probes in 300ms=%d", afterFast, slow)
	}
}

var watched = Schedule{Fast: time.Second, FastEvery: 10 * time.Millisecond, SlowEvery: 30 * time.Millisecond, Timeout: 150 * time.Millisecond, Health: 20 * time.Millisecond, Debounce: 30 * time.Millisecond}

// 代理中途消失（v2rayN 重启/退出）：回到 Waiting（中性），超时才 Down，恢复后再次 OnReady。
func TestProxyLostMidRunWaitsAgainAndRecovers(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	var ready atomic.Int64
	w := New(watched)
	defer w.Stop()
	w.OnReady(func() { ready.Add(1) })
	w.Start("a:1", func(context.Context) error {
		if up.Load() {
			return nil
		}
		return errors.New("refused")
	})
	waitState(t, w, Ready)
	up.Store(false)
	waitState(t, w, Waiting)
	if w.Ready() {
		t.Fatal("lost proxy must not count as ready")
	}
	waitState(t, w, Down)
	up.Store(true)
	waitState(t, w, Ready)
	for i := 0; i < 200 && ready.Load() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if n := ready.Load(); n != 2 {
		t.Fatalf("OnReady must run again after recovery, got %d", n)
	}
}

// 短暂抖动（Debounce 内恢复）不改状态。
func TestBriefBlipDoesNotFlap(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	s := watched
	s.Health, s.Debounce = time.Hour, 80*time.Millisecond
	w := New(s)
	defer w.Stop()
	w.Start("a:1", func(context.Context) error {
		if up.Load() {
			return nil
		}
		return errors.New("refused")
	})
	up.Store(false)
	w.Kick()
	time.Sleep(20 * time.Millisecond)
	up.Store(true)
	end := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(end) {
		if st, _, _ := w.State(); st != Ready {
			t.Fatalf("brief blip flapped the state to %v", st)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// 拨号失败（Kick）立即复查，不等定时看护。
func TestKickDetectsLossWithoutWaitingForHealthTick(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	s := watched
	s.Health, s.Debounce = time.Hour, 10*time.Millisecond
	w := New(s)
	defer w.Stop()
	w.Start("a:1", func(context.Context) error {
		if up.Load() {
			return nil
		}
		return errors.New("refused")
	})
	time.Sleep(10 * time.Millisecond)
	up.Store(false)
	w.Kick()
	waitState(t, w, Waiting)
}
