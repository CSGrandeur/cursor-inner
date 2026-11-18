package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"
)

func drainReadResult(id uint32) *cursorpb.AgentClientMessage {
	return &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: &cursorpb.ExecClientMessage{Id: id, Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/fixture", TotalLines: 1, Output: &cursorpb.ReadSuccess_Content{Content: "safe-fixture"}}}}}}}}
}
func drainClose(id uint32) *cursorpb.AgentClientMessage {
	return &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_StreamClose{StreamClose: &cursorpb.ExecClientStreamClose{Id: id}}}}}
}

// Captured arrival shape: parallel Read close precedes its result by 20/13ms.
// The producer has closed, but separate transport appends are not a result barrier.
func TestExecCloseDrainParallelReadWireOrder(t *testing.T) {
	for _, transport := range []string{"bidi", "websocket"} {
		for _, order := range []string{"close-first", "result-first"} {
			t.Run(transport+"/"+order, func(t *testing.T) {
				history := NewHistory(t.TempDir())
				conv := "fixture-conversation"
				s := newSession("live", config.Model{ID: "fixture"}, &cursorpb.AgentRunRequest{ConversationId: &conv}, history)
				h := New(nil, history)
				h.slotLocked("live").session = s
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var wg sync.WaitGroup
				defer wg.Wait()
				defer cancel()
				calls := []provider.ToolCall{{ID: "read-one", Name: "Read", Arguments: `{"path":"/fixture-one"}`}, {ID: "read-two", Name: "Read", Arguments: `{"path":"/fixture-two"}`}}
				sends := map[uint32]int{}
				out, err := s.runParallel(ctx, func(m *cursorpb.AgentServerMessage) error {
					exec := m.GetExecServerMessage()
					if exec == nil {
						return nil
					}
					id := exec.GetId()
					sends[id]++
					s.execWritten(id, nil)
					if order == "result-first" {
						replayExecWire(t, h, transport, "live", "old", uint64(id)*2, drainReadResult(id))
						replayExecWire(t, h, transport, "live", "old", uint64(id)*2+1, drainClose(id))
						return nil
					}
					replayExecWire(t, h, transport, "live", "old", uint64(id)*2, drainClose(id))
					delay := 20 * time.Millisecond
					if id == 2 {
						delay = 13 * time.Millisecond
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						timer := time.NewTimer(delay)
						defer timer.Stop()
						select {
						case <-ctx.Done():
							return
						case <-timer.C:
							replayExecWire(t, h, transport, "live", "old", uint64(id)*2+1, drainReadResult(id))
						}
					}()
					return nil
				}, calls)
				if err != nil {
					t.Fatal(err)
				}
				if len(out) != 2 {
					t.Fatalf("outcomes=%d", len(out))
				}
				messages := []provider.Message{{Role: "user", Content: "fixture"}, {Role: "assistant", ToolCalls: calls}}
				for _, o := range out {
					if o.pause != "" || o.isErr {
						t.Fatalf("normal reordered Read paused: call=%s pause=%t error=%t", o.call.ID, o.pause != "", o.isErr)
					}
					messages = append(messages, toolResult(o))
				}
				if sends[1] != 1 || sends[2] != 1 {
					t.Fatalf("unexpected retransmit: %v", sends)
				}
				if !s.hasPendingExec() {
					t.Fatal("result applied before checkpoint")
				}
				// A failed checkpoint transmission before SaveState must not mark applied.
				err = s.checkpoint(func(*cursorpb.AgentServerMessage) error { return errors.New("fixture disconnected") }, conv, "fixture", messages, "", cursorpb.AgentMode_AGENT_MODE_UNSPECIFIED)
				if err == nil || !s.hasPendingExec() {
					t.Fatal("failed checkpoint committed execution")
				}
				if err = s.checkpoint(func(*cursorpb.AgentServerMessage) error { return nil }, conv, "fixture", messages, "", cursorpb.AgentMode_AGENT_MODE_UNSPECIFIED); err != nil {
					t.Fatal(err)
				}
				if s.hasPendingExec() {
					t.Fatal("confirmed checkpoint left unapplied jobs")
				}
				if _, err = os.Stat(s.pendingExecPath()); !os.IsNotExist(err) {
					t.Fatalf("pending marker not removed: %v", err)
				}
				restored := newSession("live", s.Model, s.run, NewHistory(filepath.Dir(history.dir)))
				if restored.hasPendingExec() {
					t.Fatal("applied jobs revived after restart")
				}
			})
		}
	}
}

func TestExecCloseDrainBoundsAndSignals(t *testing.T) {
	t.Run("duplicate-close-heartbeat-and-resume-do-not-renew", func(t *testing.T) {
		h, s := lifecycleHub()
		lifecycleControl(h, 1, "close")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					lifecycleControl(h, 1, "heartbeat")
					lifecycleControl(h, 1, "close")
				}
			}
		}()
		start := time.Now()
		_, err := s.waitExec(ctx, map[uint32]int{1: 0}, 5*time.Millisecond, 3*time.Second, false)
		var e *execWaitError
		if !errors.As(err, &e) || e.reason != "stream_closed_without_terminal_result" || time.Since(start) < execCloseDrainLimit || time.Since(start) > execCloseDrainLimit+400*time.Millisecond {
			t.Fatalf("drain bound: elapsed=%v err=%v", time.Since(start), err)
		}
		start = time.Now()
		_, err = s.waitExec(ctx, map[uint32]int{1: 0}, 5*time.Millisecond, 3*time.Second, false)
		if !errors.As(err, &e) || e.reason != "stream_closed_without_terminal_result" || time.Since(start) > 100*time.Millisecond {
			t.Fatal("expired close drain renewed")
		}
	})
	t.Run("absolute-total-caps-drain", func(t *testing.T) {
		h, s := lifecycleHub()
		lifecycleControl(h, 1, "close")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		_, err := s.waitExec(ctx, map[uint32]int{1: 0}, 5*time.Millisecond, 30*time.Millisecond, false)
		var e *execWaitError
		if !errors.As(err, &e) || e.reason != "execution_total_limit" || time.Since(start) > 200*time.Millisecond {
			t.Fatalf("total bound: %v", err)
		}
	})
	for _, signal := range []string{"cancel", "throw"} {
		t.Run(signal+"-interrupts-drain", func(t *testing.T) {
			h, s := lifecycleHub()
			lifecycleControl(h, 1, "close")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			defer func() { cancel(); <-done }()
			go func() {
				defer close(done)
				timer := time.NewTimer(10 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					if signal == "cancel" {
						cancel()
					} else {
						lifecycleControl(h, 1, "throw")
					}
				}
			}()
			start := time.Now()
			_, err := s.waitExec(ctx, map[uint32]int{1: 0}, time.Second, 2*time.Second, false)
			if time.Since(start) > 200*time.Millisecond {
				t.Fatal("signal waited for drain")
			}
			if signal == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				s.deliver(drainReadResult(1).GetExecClientMessage())
				if s.queuedExecs() != 1 {
					t.Fatal("cancel lost late result")
				}
			} else {
				var e *execWaitError
				if !errors.As(err, &e) || e.reason != "client_throw" {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("queued-real-terminal-wins", func(t *testing.T) {
		h, s := lifecycleHub()
		s.deliver(drainReadResult(1).GetExecClientMessage())
		lifecycleControl(h, 1, "close")
		lifecycleControl(h, 1, "throw")
		m, err := s.waitExec(context.Background(), map[uint32]int{1: 0}, time.Nanosecond, time.Nanosecond, false)
		if err != nil || m.GetReadResult().GetSuccess() == nil {
			t.Fatalf("queued terminal lost: %v", err)
		}
	})
}

func TestExecCloseDrainDisconnectRestoreAndLateStream(t *testing.T) {
	history := NewHistory(t.TempDir())
	conv := "fixture-conversation"
	run := &cursorpb.AgentRunRequest{ConversationId: &conv}
	s := newSession("live", config.Model{ID: "fixture"}, run, history)
	call := provider.ToolCall{ID: "safe-shell", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`}
	exec, ui, pending, err := tools.Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	s.nextID = 1
	ctx, cancel := context.WithCancel(context.Background())
	_, err = s.execLoop(ctx, func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage() != nil {
			s.deliverControl(drainClose(1).GetExecClientControlMessage())
			cancel()
		}
		return nil
	}, call, exec, ui, pending)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	b := s.book()
	b.mu.Lock()
	first := b.records[1].closedAt
	b.mu.Unlock()
	restored := newSession("live", s.Model, run, NewHistory(filepath.Dir(history.dir)))
	rb := restored.book()
	rb.mu.Lock()
	loaded := rb.records[1].closedAt
	rb.mu.Unlock()
	if first.IsZero() || !loaded.Equal(first) {
		t.Fatal("restart changed first close time")
	}
	restored.deliverControl(drainClose(1).GetExecClientControlMessage())
	rb.mu.Lock()
	duplicate := rb.records[1].closedAt
	rb.mu.Unlock()
	if !duplicate.Equal(first) {
		t.Fatal("duplicate close renewed persisted drain")
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	done := make(chan struct{})
	defer func() { stop(); <-done }()
	go func() {
		defer close(done)
		timer := time.NewTimer(20 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-waitCtx.Done():
			return
		case <-timer.C:
		}
		for _, st := range []*cursorpb.ShellStream{{Event: &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}}, {Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "safe-fixture"}}}, {Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0}}}} {
			restored.deliver(&cursorpb.ExecClientMessage{Id: 1, Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: st}})
		}
	}()
	sends := 0
	messages, pause, err := restored.resumeExecJobs(waitCtx, func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage() != nil {
			sends++
		}
		return nil
	}, nil)
	if err != nil || pause != "" || sends != 0 {
		t.Fatalf("restore: pause=%t sends=%d err=%v", pause != "", sends, err)
	}
	if err = restored.checkpoint(func(*cursorpb.AgentServerMessage) error { return nil }, conv, "fixture", messages, "", cursorpb.AgentMode_AGENT_MODE_UNSPECIFIED); err != nil {
		t.Fatal(err)
	}
	if restored.hasPendingExec() {
		t.Fatal("restored terminal not applied")
	}
	if _, err = os.Stat(restored.pendingExecPath()); !os.IsNotExist(err) {
		t.Fatal("restored marker not cleared")
	}
}
