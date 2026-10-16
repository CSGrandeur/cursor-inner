package takeover

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)} }
func want(t *testing.T, got Status, state, reason string) {
	t.Helper()
	if got.State != state || got.Reason != reason {
		t.Fatalf("got %s/%s, want %s/%s", got.State, got.Reason, state, reason)
	}
}

func TestStartingIsPendingNeverError(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	want(t, tr.Judge(true, false, false), StatePending, ReasonStarting)
	c.add(30 * time.Second)
	want(t, tr.Judge(true, false, false), StatePending, ReasonStarting)
	want(t, tr.Judge(true, true, false), StateActive, "")
}

func TestStartingTimesOutToError(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	c.add(DefaultPendingMax)
	want(t, tr.Judge(true, false, false), StateError, ReasonTimeout)
}

func TestHandoverIsPendingEvenWhenInactive(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	tr.SetPhase(PhaseSettled)
	want(t, tr.Judge(true, true, false), StateActive, "")
	tr.SetPhase(PhaseHandover) // 旧进程交出：mitm 暂时不在运行
	c.add(5 * time.Second)
	want(t, tr.Judge(true, false, false), StatePending, ReasonHandover)
	// 交接回滚：回到 settled，从此刻起给宽限
	tr.SetPhase(PhaseSettled)
	want(t, tr.Judge(true, false, false), StatePending, ReasonSettling)
	c.add(DefaultGrace - time.Second)
	want(t, tr.Judge(true, false, false), StatePending, ReasonSettling)
	c.add(2 * time.Second)
	want(t, tr.Judge(true, false, false), StateError, ReasonNotActive)
	want(t, tr.Judge(true, true, false), StateActive, "") // 恢复后立刻转绿
}

// 接过接管之后的第一次读取：settled 后立刻 active，不会出现红灯。
func TestAdoptedHandoverNeverShowsError(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	tr.SetPhase(PhaseHandover)
	for i := 0; i < 20; i++ {
		c.add(500 * time.Millisecond)
		if s := tr.Judge(true, false, false); s.State == StateError {
			t.Fatalf("handover must not be an error: %+v", s)
		}
	}
	tr.SetPhase(PhaseSettled)
	want(t, tr.Judge(true, true, false), StateActive, "")
}

func TestProblemIsErrorImmediately(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	want(t, tr.Judge(true, false, true), StateError, ReasonProblem)
	tr.SetPhase(PhaseSettled)
	want(t, tr.Judge(true, true, true), StateError, ReasonProblem) // 生效了但有错误文本（如没能结束 Cursor）
}

func TestOffAndSkipped(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	want(t, tr.Judge(false, false, false), StateOff, "")
	tr.SetSkipped(true)
	tr.SetPhase(PhaseSettled)
	want(t, tr.Judge(true, false, false), StateOff, ReasonSkipped)
}

// 长时间正常运行后接管掉线：仍给一次宽限，防止抖动，之后判失败。
func TestLongRunningDropGetsGraceThenError(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	tr.SetPhase(PhaseSettled)
	c.add(24 * time.Hour)
	want(t, tr.Judge(true, true, false), StateActive, "")
	c.add(time.Minute)
	want(t, tr.Judge(true, false, false), StatePending, ReasonSettling)
	c.add(DefaultGrace)
	want(t, tr.Judge(true, false, false), StateError, ReasonNotActive)
}

func TestSinceStaysWhileStateUnchanged(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.now)
	first := tr.Judge(true, false, false)
	c.add(3 * time.Second)
	second := tr.Judge(true, false, false)
	if !first.Since.Equal(second.Since) {
		t.Fatalf("since moved while state unchanged: %v -> %v", first.Since, second.Since)
	}
	tr.SetPhase(PhaseHandover)
	if third := tr.Judge(true, false, false); !third.Since.After(first.Since) {
		t.Fatalf("since must move when the reason changes")
	}
}
