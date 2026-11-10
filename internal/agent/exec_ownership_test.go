package agent

import (
	"context"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"
)

func TestOriginalExecStreamReachesIssuingSession(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"shell-one","function":{"name":"Shell","arguments":"{\"command\":\"printf fresh-result\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"done"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, id == "mine" }, NewHistory(t.TempDir()))
	old := newSession("old", model, runRequest("mine", "conv", "old", nil).GetRunRequest(), hub.history)
	old.finished.Store(true)
	hub.finished["old"] = &finished{session: old, at: time.Now()}
	env := &cursorpb.RequestContextEnv{OsVersion: "linux", WorkspacePaths: []string{"/w"}}
	hub.Client("new", runRequest("mine", "conv", "new", env))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	current, err := hub.Wait(ctx, "new")
	if err != nil {
		t.Fatal(err)
	}
	terminalCards := 0
	err = current.Run(ctx, dialer.Direct(), func(message *cursorpb.AgentServerMessage) error {
		if completed := message.GetInteractionUpdate().GetToolCallCompleted(); completed != nil && completed.GetToolCall().GetShellToolCall() != nil {
			terminalCards++
			if completed.GetToolCall().GetShellToolCall().GetResult().GetSuccess() == nil {
				t.Error("real shell result missing")
			}
		}
		exec := message.GetExecServerMessage()
		if exec == nil {
			return nil
		}
		send := func(result *cursorpb.ExecClientMessage) {
			result.Id = exec.GetId()
			result.ExecId = exec.GetExecId()
			route, err := hub.BidiWithOriginal(bidi(t, "new", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: result}}), "old")
			if err != nil || !route.Local {
				t.Errorf("delivery: %+v %v", route, err)
			}
			if hub.slots["new"].session != current {
				t.Error("active binding replaced")
			}
		}
		switch {
		case exec.GetRequestContextArgs() != nil:
			send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: env}}}}}})
		case exec.GetShellStreamArgs() != nil:
			for _, stream := range []*cursorpb.ShellStream{
				{Event: &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}},
				{Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "fresh-result"}}},
				{Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0}}},
			} {
				send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: stream}})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminalCards != 1 || len(old.inbox) != 0 {
		t.Fatalf("cards=%d old inbox=%d", terminalCards, len(old.inbox))
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.requests) != 2 || !strings.Contains(fm.requests[1], "fresh-result") {
		t.Fatalf("model did not consume shell result: %d calls", len(fm.requests))
	}
}

func TestOriginalIOAliasDoesNotAdoptOrGuess(t *testing.T) {
	hub := New(nil, nil)
	source := newSession("source", config.Model{}, nil, nil)
	target := newSession("target", config.Model{}, nil, nil)
	source.execs.Store(uint32(1), "source-exec")
	target.execs.Store(uint32(1), "target-exec")
	hub.slotLocked("source").session = source
	hub.slotLocked("target").session = target
	routeExec := func(id, exec string) {
		t.Helper()
		_, err := hub.BidiWithOriginal(bidi(t, id, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: &cursorpb.ExecClientMessage{Id: 1, ExecId: exec}}}), "source")
		if err != nil {
			t.Fatal(err)
		}
	}
	routeExec("target", "target-exec")
	routeExec("target", "source-exec")
	routeExec("target", "unknown-exec")
	routeExec("transport-alias", "source-exec")
	_, err := hub.Bidi(bidi(t, "transport-alias", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: &cursorpb.ExecClientMessage{Id: 1, ExecId: "source-exec"}}}))
	if err != nil || !hub.Owns("transport-alias") {
		t.Fatal("transport alias not retained", err)
	}
	if len(target.inbox) != 1 || len(source.inbox) != 3 {
		t.Fatalf("target=%d source=%d", len(target.inbox), len(source.inbox))
	}
	if hub.slots["target"].session != target || hub.slots["transport-alias"] != nil {
		t.Fatal("I/O rebound a slot")
	}
}

func TestOriginalHeartbeatCannotBindARequest(t *testing.T) {
	hub := New(nil, nil)
	source := newSession("old", config.Model{}, nil, nil)
	source.finished.Store(true)
	hub.finished["old"] = &finished{session: source, at: time.Now()}
	route, err := hub.BidiWithOriginal(bidi(t, "heartbeat", &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &cursorpb.ClientHeartbeat{}}}), "old")
	if err != nil || route.Local || hub.slots["heartbeat"] != nil {
		t.Fatalf("heartbeat adopted: %+v %v", route, err)
	}
}

func TestOriginalRunRestoresOnlyUnboundFinishedSession(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "disk"}[disk], func(t *testing.T) {
			history := NewHistory(t.TempDir())
			hub := New(nil, history)
			conv := "conversation"
			old := newSession("old", config.Model{}, &cursorpb.AgentRunRequest{ConversationId: &conv}, history)
			old.baseState = &cursorpb.ConversationStateStructure{}
			old.finished.Store(true)
			hub.slotLocked("old").session = old
			if err := history.SaveState(conv, old.baseState); err != nil {
				t.Fatal(err)
			}
			hub.Done("old")
			if disk {
				hub = New(nil, history)
			}
			msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{ConversationId: &conv, Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}}}}}
			route, err := hub.BidiWithOriginal(bidi(t, "resume", msg), "old")
			if err != nil || !route.Local {
				t.Fatalf("resume: %+v %v", route, err)
			}
			got, err := hub.Wait(context.Background(), "resume")
			if err != nil || !got.Finished() || got.ConversationID() != conv {
				t.Fatal("restoration failed", err)
			}
			if !disk && got != old {
				t.Fatal("memory resume replaced Session")
			}
		})
	}
}

func TestUnconfirmedShellDoesNotCompleteOrPersistSuccess(t *testing.T) {
	call := provider.ToolCall{ID: "wait", Name: "Shell", Arguments: `{"command":"echo ok"}`}
	exec, ui, pending, err := tools.Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	pending.Wait = time.Millisecond
	session := newSession("run", config.Model{}, nil, nil)
	completed := 0
	out, err := session.execLoop(context.Background(), func(m *cursorpb.AgentServerMessage) error {
		if m.GetInteractionUpdate().GetToolCallCompleted() != nil {
			completed++
		}
		return nil
	}, call, exec, ui, pending)
	if err != nil || !out.isErr || completed != 0 || ui.GetShellToolCall().GetResult() != nil {
		t.Fatalf("timeout fabricated result/completion: %+v %v count=%d", out, err, completed)
	}
	if !strings.Contains(out.text, "unknown") {
		t.Fatal(out.text)
	}
	// Checkpoint must retain the uncertainty as text, never as a resultless Ran card.
	messages := []provider.Message{{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, toolResult(out)}
	steps, err := encodeTurnSteps(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps=%d", len(steps))
	}
	var step cursorpb.ConversationStep
	if err := proto.Unmarshal(steps[0], &step); err != nil {
		t.Fatal(err)
	}
	if step.GetToolCall() != nil || !strings.Contains(step.GetAssistantMessage().GetText(), "unknown") {
		t.Fatal("checkpoint fabricated completed shell", &step)
	}
}

func TestOriginalCannotCaptureExplicitOfficialRun(t *testing.T) {
	hub := New(func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	conv := "conv"
	old := newSession("old", config.Model{}, &cursorpb.AgentRunRequest{ConversationId: &conv}, nil)
	old.finished.Store(true)
	f := &finished{session: old, at: time.Now()}
	hub.finished["old"] = f
	hub.byConv[conv] = f
	request := runRequest("official", conv, "", nil)
	request.GetRunRequest().Action = &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}}
	route, err := hub.BidiWithOriginal(bidi(t, "official-new", request), "old")
	if err != nil || route.Local || hub.slots["official-new"].session != nil {
		t.Fatalf("official captured: %+v %v", route, err)
	}
}

func TestBoundPendingSessionCannotBeReplaced(t *testing.T) {
	hub := New(nil, nil)
	old := newSession("old", config.Model{}, nil, nil)
	old.finished.Store(true)
	hub.finished["old"] = &finished{session: old, at: time.Now()}
	pending := newSession("new", config.Model{}, nil, nil)
	hub.slotLocked("new").session = pending
	hub.NoteResume("old", "new")
	if hub.slots["new"].session != pending {
		t.Fatal("not-yet-running Session was replaced")
	}
	resume := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}}}}}
	if _, err := hub.BidiWithOriginal(bidi(t, "new", resume), "old"); err != nil {
		t.Fatal(err)
	}
	if pending.run != nil {
		t.Fatal("not-yet-running Session action was overwritten")
	}
	pending.running.Store(1)
	if err := pending.Run(context.Background(), nil, nil); err == nil {
		t.Fatal("duplicate model runner accepted")
	}
}

func TestLiveResumeLinksWithoutAdoptingOrStartingAgain(t *testing.T) {
	hub := New(nil, nil)
	conv := "conv"
	run := &cursorpb.AgentRunRequest{ConversationId: &conv}
	live := newSession("live", config.Model{}, run, nil)
	live.running.Store(1)
	hub.slotLocked("live").session = live
	request := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{ConversationId: &conv, Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}}}}}
	route, err := hub.BidiWithOriginal(bidi(t, "reconnect", request), "live")
	if err != nil || !route.Local || hub.slots["reconnect"].session != live || live.run != run {
		t.Fatal("live resume adopted or lost owner", route, err)
	}
	if hub.Active() != 1 {
		t.Fatal("aliased live run counted more than once")
	}
}

func TestCancelledShellDoesNotEmitCompletion(t *testing.T) {
	call := provider.ToolCall{ID: "cancel", Name: "Shell", Arguments: `{"command":"echo ok"}`}
	exec, ui, pending, err := tools.Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	session := newSession("cancel", config.Model{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	completed := 0
	_, err = session.execLoop(ctx, func(m *cursorpb.AgentServerMessage) error {
		if m.GetInteractionUpdate().GetToolCallCompleted() != nil {
			completed++
		}
		return nil
	}, call, exec, ui, pending)
	if err == nil || completed != 0 || ui.GetShellToolCall().GetResult() != nil {
		t.Fatal("cancel fabricated result/completion", err, completed)
	}
}
