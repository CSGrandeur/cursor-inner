package agent

import (
	"context"
	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"
	"errors"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lifecycleHub() (*Hub, *Session) {
	h := New(nil, nil)
	s := newSession("live", config.Model{}, nil, nil)
	h.slotLocked("live").session = s
	s.execs.Store(uint32(1), "one")
	s.execs.Store(uint32(2), "two")
	return h, s
}
func lifecycleControl(h *Hub, id uint32, kind string) {
	c := &cursorpb.ExecClientControlMessage{}
	switch kind {
	case "heartbeat":
		c.Message = &cursorpb.ExecClientControlMessage_Heartbeat{Heartbeat: &cursorpb.ExecClientHeartbeat{Id: id}}
	case "close":
		c.Message = &cursorpb.ExecClientControlMessage_StreamClose{StreamClose: &cursorpb.ExecClientStreamClose{Id: id}}
	case "throw":
		c.Message = &cursorpb.ExecClientControlMessage_Throw{Throw: &cursorpb.ExecClientThrow{Id: id, Error: "synthetic executor failure"}}
	}
	h.Client("live", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: c}})
}
func TestExecLifecycleHeartbeatIsNotSilence(t *testing.T) {
	h, s := lifecycleHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		end := time.NewTimer(100 * time.Millisecond)
		defer end.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				lifecycleControl(h, 1, "heartbeat")
			case <-end.C:
				s.deliver(&cursorpb.ExecClientMessage{Id: 1})
				return
			}
		}
	}()
	m, err := s.awaitTool(ctx, &tools.Pending{Wait: 25 * time.Millisecond}, 1)
	if err != nil || m == nil {
		t.Fatalf("active exec misclassified as silence: %v", err)
	}
}
func TestExecLifecycleControlTerminalWakesWaiter(t *testing.T) {
	for _, kind := range []string{"throw", "close"} {
		t.Run(kind, func(t *testing.T) {
			h, s := lifecycleHub()
			lifecycleControl(h, 1, kind)
			ctx, cancel := context.WithTimeout(context.Background(), execCloseDrainLimit+500*time.Millisecond)
			defer cancel()
			_, err := s.awaitTool(ctx, nil, 1)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s ignored; expected explicit protocol terminal error, got %v", kind, err)
			}
		})
	}
}
func TestExecLifecycleOtherExecFramesKeepOrder(t *testing.T) {
	_, s := lifecycleHub()
	for _, v := range []string{"start", "stdout", "exit"} {
		st := &cursorpb.ShellStream{}
		switch v {
		case "start":
			st.Event = &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}
		case "stdout":
			st.Event = &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "safe-fixture"}}
		case "exit":
			st.Event = &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{}}
		}
		s.deliver(&cursorpb.ExecClientMessage{Id: 2, Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: st}})
	}
	s.deliver(&cursorpb.ExecClientMessage{Id: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.await(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"start", "stdout", "exit"} {
		m, err := s.await(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		st := m.GetShellStream()
		got := ""
		if st.GetStart() != nil {
			got = "start"
		}
		if st.GetStdout() != nil {
			got = "stdout"
		}
		if st.GetExit() != nil {
			got = "exit"
		}
		if got != want {
			t.Fatalf("wanted %s, got %s (early stream overwritten)", want, got)
		}
	}
}

func TestExecLifecycleWireHeartbeatReplay(t *testing.T) {
	for _, transport := range []string{"bidi", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			h, s := lifecycleHub()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			defer func() { cancel(); <-done }()
			go func() {
				defer close(done)
				ticker := time.NewTicker(4 * time.Millisecond)
				defer ticker.Stop()
				end := time.NewTimer(90 * time.Millisecond)
				defer end.Stop()
				var seq uint64
				for {
					select {
					case <-ctx.Done():
						return
					case <-end.C:
						s.deliver(&cursorpb.ExecClientMessage{Id: 1})
						return
					case <-ticker.C:
						msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_Heartbeat{Heartbeat: &cursorpb.ExecClientHeartbeat{Id: 1}}}}}
						raw, _ := proto.Marshal(msg)
						if transport == "bidi" {
							body := protox.AppendString(nil, 1, protox.EncodeHex(raw))
							body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, "live"))
							_, _ = h.BidiWithOriginal(protox.Frame(0, body), "old")
						} else {
							wire := protox.EncodeAgentServer("live", seq, raw)
							seq++
							frame := protox.ClassifyAgentPayload(wire)
							decoded := &cursorpb.AgentClientMessage{}
							_ = proto.Unmarshal(frame.Message, decoded)
							h.Client(frame.RequestID, decoded)
						}
					}
				}
			}()
			if _, err := s.awaitTool(ctx, &tools.Pending{Wait: 20 * time.Millisecond}, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestExecLifecycleHeartbeatCannotRenewAnotherExec(t *testing.T) {
	h, s := lifecycleHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				lifecycleControl(h, 1, "heartbeat")
			}
		}
	}()
	_, err := s.awaitTool(ctx, &tools.Pending{Wait: 20 * time.Millisecond}, 2)
	var e *execWaitError
	if !errors.As(err, &e) || e.id != 2 || e.reason != "inactive" {
		t.Fatalf("cross-exec lease renewed: %v", err)
	}
}
func TestExecLifecycleTotalBoundAndCancellation(t *testing.T) {
	old := execTotalLimit
	execTotalLimit = 30 * time.Millisecond
	defer func() { execTotalLimit = old }()
	h, s := lifecycleHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				lifecycleControl(h, 1, "heartbeat")
			}
		}
	}()
	_, err := s.awaitTool(ctx, &tools.Pending{Wait: 20 * time.Millisecond}, 1)
	var e *execWaitError
	if !errors.As(err, &e) || e.reason != "execution_total_limit" {
		t.Fatalf("heartbeat bypassed absolute limit: %v", err)
	}
	c, stop := context.WithCancel(context.Background())
	stop()
	s.deliver(&cursorpb.ExecClientMessage{Id: 2})
	if _, err := s.await(c, 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.await(context.Background(), 2); err != nil {
		t.Fatal("cancellation consumed a queued result", err)
	}
}
func TestExecLifecycleJournalRecoversLateShellWithoutRedispatch(t *testing.T) {
	history := NewHistory(t.TempDir())
	run := runRequest("mine", "conv", "run", nil).GetRunRequest()
	s := newSession("live", config.Model{}, run, history)
	call := provider.ToolCall{ID: "shell", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`}
	exec, ui, pending, err := tools.Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	s.nextID = 1
	ctx, cancel := context.WithCancel(context.Background())
	_, err = s.execLoop(ctx, func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage() != nil {
			s.deliver(&cursorpb.ExecClientMessage{Id: 1, Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{Event: &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}}}})
			s.deliver(&cursorpb.ExecClientMessage{Id: 1, Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "safe-fixture"}}}}})
			cancel()
		}
		return nil
	}, call, exec, ui, pending)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	restored := newSession("live", config.Model{}, run, NewHistory(filepath.Dir(history.dir)))
	// A new process restores only via a RunRequest; I/O itself does not adopt.
	restored.deliver(&cursorpb.ExecClientMessage{Id: 1, Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{}}}}})
	restored.deliverControl(&cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_StreamClose{StreamClose: &cursorpb.ExecClientStreamClose{Id: 1}}})
	dispatched := 0
	messages, pause, err := restored.resumeExecJobs(context.Background(), func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage() != nil {
			dispatched++
		}
		return nil
	}, nil)
	if err != nil || pause != "" || dispatched != 0 || len(messages) < 2 || !strings.Contains(messages[len(messages)-1].Content, "safe-fixture") {
		t.Fatalf("recovery err=%v pause=%q dispatch=%d messages=%d", err, pause, dispatched, len(messages))
	}
	restored.markExecApplied(messages)
	if restored.hasPendingExec() {
		t.Fatal("confirmed result not committed")
	}
	info, err := os.Stat(restored.journalPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal permissions: %v %v", info, err)
	}
	again := newSession("live", config.Model{}, run, NewHistory(filepath.Dir(history.dir)))
	if again.hasPendingExec() {
		t.Fatal("consumed result replayed after restart")
	}
}

// Safe synthetic values, preserving the captured field5/field1 envelopes and
// websocket request/sequence wrapper. No business command or output is replayed.
func replayExecWire(t *testing.T, h *Hub, transport, request, original string, seq uint64, m *cursorpb.AgentClientMessage) {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if transport == "bidi" {
		body := protox.AppendString(nil, 1, protox.EncodeHex(raw))
		body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, request))
		if _, err = h.BidiWithOriginal(protox.Frame(0, body), original); err != nil {
			t.Fatal(err)
		}
		return
	}
	frame := protox.ClassifyAgentPayload(protox.EncodeAgentServer(request, seq, raw))
	decoded := &cursorpb.AgentClientMessage{}
	if err = proto.Unmarshal(frame.Message, decoded); err != nil {
		t.Fatal(err)
	}
	h.Client(frame.RequestID, decoded)
}
func TestExecLifecycleShellWireReplayConsumesLateTerminal(t *testing.T) {
	for _, transport := range []string{"bidi", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			h, s := lifecycleHub()
			old := newSession("old", config.Model{}, nil, nil)
			old.finished.Store(true)
			old.execs.Store(uint32(1), "old-exec")
			h.finished["old"] = &finished{session: old, at: time.Now()}
			call := provider.ToolCall{ID: "safe-shell", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`}
			exec, ui, pending, err := tools.Request(1, call)
			if err != nil {
				t.Fatal(err)
			}
			exec.ExecId = "one"
			pending.Wait = 40 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			type result struct {
				out toolOutcome
				err error
			}
			done := make(chan result, 1)
			sent := make(chan struct{})
			go func() {
				out, e := s.execLoop(ctx, func(m *cursorpb.AgentServerMessage) error {
					if m.GetExecServerMessage() != nil {
						close(sent)
					}
					return nil
				}, call, exec, ui, pending)
				done <- result{out, e}
			}()
			<-sent
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			end := time.NewTimer(130 * time.Millisecond)
			defer end.Stop()
			var seq uint64
		loop:
			for {
				select {
				case <-ticker.C:
					replayExecWire(t, h, transport, "live", "old", seq, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_Heartbeat{Heartbeat: &cursorpb.ExecClientHeartbeat{Id: 1}}}}})
					seq++
				case <-end.C:
					break loop
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for _, st := range []*cursorpb.ShellStream{{Event: &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}}, {Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "safe-fixture"}}}, {Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0}}}} {
				replayExecWire(t, h, transport, "live", "old", seq, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: &cursorpb.ExecClientMessage{Id: 1, ExecId: "one", Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: st}}}})
				seq++
			}
			replayExecWire(t, h, transport, "live", "old", seq, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_StreamClose{StreamClose: &cursorpb.ExecClientStreamClose{Id: 1}}}}})
			got := <-done
			if got.err != nil || got.out.pause != "" || got.out.isErr || !strings.Contains(got.out.text, "safe-fixture") {
				t.Fatalf("late result not consumed: err=%v pause=%q error=%t", got.err, got.out.pause, got.out.isErr)
			}
			if h.slots["live"].session != s || old.queuedExecs() != 0 {
				t.Fatal("owner changed")
			}
		})
	}
}
func TestExecLifecyclePendingOwnerRestoredBeforeReconnect(t *testing.T) {
	root := t.TempDir()
	history := NewHistory(root)
	conv := "fixture-conversation"
	run := &cursorpb.AgentRunRequest{ConversationId: &conv}
	model := config.Model{ID: "custom"}
	s := newSession("old", model, run, history)
	call := provider.ToolCall{ID: "safe-shell", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`}
	s.beginExecJob(call, 1)
	s.execDispatched(&cursorpb.ExecServerMessage{Id: 1, ExecId: "safe-shell-1"})
	lookup := func(id string) (config.Model, bool) { return model, id == model.ID }
	h := New(lookup, NewHistory(root))
	owner := h.finished["old"].session
	if owner == nil || len(h.slots) != 0 {
		t.Fatal("detached owner not restored")
	}
	replayExecWire(t, h, "bidi", "new-transport", "old", 0, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_Heartbeat{Heartbeat: &cursorpb.ExecClientHeartbeat{Id: 1}}}}})
	if len(h.slots) != 0 {
		t.Fatal("control adopted slot")
	}
	route, _, _ := h.Client("old", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: &cursorpb.ExecClientMessage{Id: 1, ExecId: "safe-shell-1"}}})
	if !route.Local || owner.queuedExecs() != 1 || h.slots["old"].session != nil {
		t.Fatal("late result lost or adopted transport")
	}
	resumed := &cursorpb.AgentRunRequest{ConversationId: &conv, RequestedModel: &cursorpb.RequestedModel{ModelId: "custom"}}
	_, adopted, _ := h.Client("fresh-run", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: resumed}})
	if adopted != owner {
		t.Fatal("new request hid unresolved prior execution")
	}
	other := newSession("other", model, run, history)
	if !other.foreignPendingExec() {
		t.Fatal("unresolved side effect not fenced")
	}
}
func TestExecLifecycleJournalFailurePreventsDispatch(t *testing.T) {
	history := NewHistory(t.TempDir())
	if err := os.MkdirAll(history.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history.dir, "executions"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	conv := "fixture"
	s := newSession("live", config.Model{}, &cursorpb.AgentRunRequest{ConversationId: &conv}, history)
	call := provider.ToolCall{ID: "write", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`}
	exec, ui, pending, err := tools.Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	_, err = s.execLoop(context.Background(), func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage() != nil {
			n++
		}
		return nil
	}, call, exec, ui, pending)
	if err == nil || n != 0 {
		t.Fatalf("journal failed but dispatched=%d err=%v", n, err)
	}
}

func TestExecLifecycleOriginalControlsNeverAdoptOrCaptureOfficial(t *testing.T) {
	h, current := lifecycleHub()
	old := newSession("old", config.Model{}, nil, nil)
	old.finished.Store(true)
	old.execs.Store(uint32(1), "old-one")
	h.finished["old"] = &finished{session: old, at: time.Now()}
	control := func(request string, id uint32) {
		replayExecWire(t, h, "bidi", request, "old", 0, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_Heartbeat{Heartbeat: &cursorpb.ExecClientHeartbeat{Id: id}}}}})
	}
	control("live", 1)
	if h.slots["live"].session != current || h.finished["old"].session != old {
		t.Fatal("live binding overwritten")
	}
	current.book().mu.Lock()
	_, got := current.book().records[1]
	current.book().mu.Unlock()
	if !got {
		t.Fatal("control not delivered to current owner")
	}
	old.book().mu.Lock()
	n := len(old.book().records)
	old.book().mu.Unlock()
	if n != 0 {
		t.Fatal("numeric collision delivered to old owner")
	}
	control("alias", 1)
	if h.slots["alias"] != nil {
		t.Fatal("control adopted empty alias")
	}
	h.slotLocked("official").decided = true
	h.finished["official"] = &finished{session: old, at: time.Now()}
	before := old.book().record(1).activity
	control("official", 1)
	if old.book().record(1).activity != before {
		t.Fatal("official original captured")
	}
	route, _, _ := h.Client("official", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientControlMessage{ExecClientControlMessage: &cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_Heartbeat{Heartbeat: &cursorpb.ExecClientHeartbeat{Id: 1}}}}})
	if route.Local || old.book().record(1).activity != before {
		t.Fatal("explicit official route captured")
	}
	control("alias", 999)
	if _, ok := old.book().records[999]; ok {
		t.Fatal("unknown exec admitted")
	}
}
func TestExecLifecycleCloseThenLateFramesRecoverWithoutRerun(t *testing.T) {
	_, s := lifecycleHub()
	call := provider.ToolCall{ID: "safe-shell", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`}
	exec, ui, pending, err := tools.Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	emit := func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage() != nil {
			sent++
			s.deliverControl(&cursorpb.ExecClientControlMessage{Message: &cursorpb.ExecClientControlMessage_StreamClose{StreamClose: &cursorpb.ExecClientStreamClose{Id: 1}}})
		}
		return nil
	}
	out, err := s.execLoop(context.Background(), emit, call, exec, ui, pending)
	if err != nil || out.pause == "" || sent != 1 {
		t.Fatalf("missing terminal not safely paused: %v", err)
	}
	for _, st := range []*cursorpb.ShellStream{{Event: &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}}, {Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "safe-fixture"}}}, {Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0}}}} {
		s.deliver(&cursorpb.ExecClientMessage{Id: 1, Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: st}})
	}
	messages, pause, err := s.resumeExecJobs(context.Background(), emit, nil)
	if err != nil || pause != "" || sent != 1 || len(messages) < 2 || !strings.Contains(messages[len(messages)-1].Content, "safe-fixture") {
		t.Fatalf("late result recovery: err=%v pause=%q sends=%d", err, pause, sent)
	}
}

func (s *Session) queuedExecs() int {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, r := range b.records {
		n += len(r.queue)
	}
	return n
}

func TestExecLifecycleCancelledStagesAndNewActions(t *testing.T) {
	_, s := lifecycleHub()
	s.nextID = 1
	call := provider.ToolCall{ID: "edit", Name: "StrReplace", Arguments: `{"path":"/fixture.txt","old_string":"before","new_string":"after"}`}
	j := s.beginExecJob(call, 1)
	s.execDispatched(&cursorpb.ExecServerMessage{Id: 1})
	s.cancelExecJob(call.ID)
	if !s.cancelledBeforeStage(j, 1) {
		t.Fatal("cancelled edit may advance to unsent write")
	}
	next := &cursorpb.ExecServerMessage{}
	s.jobStage(j, 1, next)
	if !s.cancelledBeforeStage(j, 1) {
		t.Fatal("allocated but unsent stage may run after cancellation")
	}
	s.execDispatched(next)
	if s.cancelledBeforeStage(j, 1) {
		t.Fatal("already-dispatched write result would be hidden")
	}
	n := 0
	out, err := s.runTool(context.Background(), func(*cursorpb.AgentServerMessage) error { n++; return nil }, provider.ToolCall{ID: "new", Name: "Shell", Arguments: `{"command":"printf safe-fixture"}`})
	if err != nil || out.pause == "" || n != 0 {
		t.Fatalf("new side effect not fenced: sent=%d err=%v", n, err)
	}
}
