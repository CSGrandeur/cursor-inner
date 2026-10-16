package app

import (
	"testing"
	"time"

	"cursor-inner/internal/takeover"
)

// 热交接后第一次读取不能是错误：接入中是 pending，接上后是 active；
// 旧进程交出期间、回滚期间同理；只有宽限过后仍没生效才是 error。
func TestStateTakeoverTriState(t *testing.T) {
	a := desktopApp(t)
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	a.track = takeover.NewTracker(func() time.Time { return now })
	state := func() (string, string) {
		t.Helper()
		v, err := a.State()
		if err != nil {
			t.Fatal(err)
		}
		if !v.Takeover {
			t.Fatal("default config should have takeover on")
		}
		if v.TakeoverSince == "" {
			t.Fatal("takeover_since must be reported")
		}
		return v.TakeoverState, v.TakeoverReason
	}
	if s, r := state(); s != takeover.StatePending || r != takeover.ReasonStarting {
		t.Fatalf("fresh process: %s/%s", s, r)
	}
	a.SetTakeoverPhase(takeover.PhaseHandover)
	now = now.Add(3 * time.Second)
	if s, r := state(); s != takeover.StatePending || r != takeover.ReasonHandover {
		t.Fatalf("handing over: %s/%s", s, r)
	}
	a.SetTakeoverPhase(takeover.PhaseSettled)
	if s, r := state(); s != takeover.StatePending || r != takeover.ReasonSettling {
		t.Fatalf("just settled, not yet active: %s/%s", s, r)
	}
	now = now.Add(takeover.DefaultGrace + time.Second)
	if s, r := state(); s != takeover.StateError || r != takeover.ReasonNotActive {
		t.Fatalf("after the grace: %s/%s", s, r)
	}
	if err := a.life.ProxyOnly(); err != nil { // 调试实例的本机代理：Active 为真
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.life.Stop() })
	if s, _ := state(); s != takeover.StateActive {
		t.Fatalf("active: %s", s)
	}
	a.Freeze(true) // 交出：接管暂时不运行也不是故障
	if s, r := state(); s != takeover.StateActive && !(s == takeover.StatePending && r == takeover.ReasonHandover) {
		t.Fatalf("freeze: %s/%s", s, r)
	}
}

func TestStateTakeoverSkippedIsOffNotError(t *testing.T) {
	a := desktopApp(t)
	a.SetTakeoverSkipped(true)
	a.SetTakeoverPhase(takeover.PhaseSettled)
	v, _ := a.State()
	if v.TakeoverState != takeover.StateOff || v.TakeoverReason != takeover.ReasonSkipped {
		t.Fatalf("%s/%s", v.TakeoverState, v.TakeoverReason)
	}
}
