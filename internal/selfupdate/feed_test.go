package selfupdate

import (
	"context"
	"testing"
	"time"
)

func nextOrFail(t *testing.T, w *Watch) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, err := w.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// 同一阶段的进度合并成最新一条；阶段切换、路径变化一条不丢。
func TestWatchCoalescesProgressKeepsStages(t *testing.T) {
	u := New(Options{Version: "v1.0.0"})
	w := u.Watch()
	defer w.Close()
	if st := nextOrFail(t, w); st.State != StateIdle {
		t.Fatalf("first snapshot %+v", st)
	}
	u.set(func(s *Status) { s.State, s.Total = StateDownloading, 1000 })
	for i := 1; i <= 50; i++ {
		u.set(func(s *Status) { s.Done, s.Speed, s.DLRoute = int64(i*10), 100, "default" })
	}
	u.set(func(s *Status) { s.DLProbe = "proxy" })
	u.set(func(s *Status) { s.Done = 600 })
	u.set(func(s *Status) { s.DLProbe, s.DLRoute = "", "proxy" })
	u.set(func(s *Status) { s.State = StateVerifying })
	var got []Status
	for range 5 {
		got = append(got, nextOrFail(t, w))
	}
	// downloading(无路径) → downloading(default，合并后是最后一条 500) → probe(合并到 600) → 换到 proxy → verifying
	if got[0].DLRoute != "" || got[1].Done != 500 || got[1].DLRoute != "default" || got[2].DLProbe != "proxy" || got[2].Done != 600 || got[3].DLRoute != "proxy" || got[4].State != StateVerifying {
		for _, s := range got {
			t.Logf("%s route=%s probe=%s done=%d", s.State, s.DLRoute, s.DLProbe, s.Done)
		}
		t.Fatal("unexpected sequence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := w.Next(ctx); err == nil {
		t.Fatal("extra snapshot")
	}
}

// 百分比向下取整、剩余时间向上取整，都在发布时算好。
func TestPublishDerivesPercentAndETA(t *testing.T) {
	u := New(Options{Version: "v1.0.0"})
	cases := []struct {
		done, total, speed int64
		pct                int
		eta                int64
	}{
		{0, 1000, 0, 0, 0},
		{999, 1000, 0, 99, 0},
		{1000, 1000, 50, 100, 0},
		{250, 1000, 100, 25, 8},
		{1, 3, 1, 33, 2},
		{2000, 1000, 10, 100, 0}, // 越界按满算
		{5, 0, 10, 0, 0},
	}
	for _, c := range cases {
		u.set(func(s *Status) { s.Done, s.Total, s.Speed = c.done, c.total, c.speed })
		st := u.Status()
		if st.Percent != c.pct || st.ETA != c.eta {
			t.Fatalf("%d/%d @%d: pct=%d eta=%d, want %d %d", c.done, c.total, c.speed, st.Percent, st.ETA, c.pct, c.eta)
		}
	}
}

func TestWatchCloseUnblocksNext(t *testing.T) {
	u := New(Options{Version: "v1.0.0"})
	w := u.Watch()
	nextOrFail(t, w)
	done := make(chan error, 1)
	go func() {
		_, err := w.Next(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	w.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Next returned a snapshot after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Next still blocked after Close")
	}
	u.set(func(s *Status) { s.State = StateChecking }) // 关闭后不再推送，也不 panic
	if len(u.watchers) != 0 {
		t.Fatal("watcher not removed")
	}
}
