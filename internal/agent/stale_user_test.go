package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

func TestPreferRestoredHistoryRewindWithNewQuestion(t *testing.T) {
	disk := []provider.Message{
		{Role: "user", Content: "keep me", Shown: "keep me"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "junk later", Shown: "junk later"},
	}
	if !preferRestoredHistory(disk, disk[:2], &cursorpb.UserMessage{Text: "new question"}) {
		t.Fatal("a shorter request state with a genuinely new question should rewind")
	}
}

func TestPreferRestoredHistoryKeepsLongerDiskWhenUserIsStale(t *testing.T) {
	disk := []provider.Message{
		{Role: "user", Content: "first", Shown: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "older question", Shown: "older question"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "latest question", Shown: "latest question"},
		{Role: "assistant", Content: "working"},
		{Role: "tool", Content: "done", ToolCallID: "c1"},
	}
	restored := disk[:4]
	if preferRestoredHistory(disk, restored, &cursorpb.UserMessage{Text: "older question"}) {
		t.Fatal("must not replace a longer saved history with a stale earlier user message")
	}
	if preferRestoredHistory(disk, restored, nil) {
		t.Fatal("a resume with a shorter request state must keep the longer disk history")
	}
}

func TestApplySkipsSupersededUser(t *testing.T) {
	p := planTurn(&cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
		UserMessage: &cursorpb.UserMessage{Text: "older question"},
	}}})
	p.skipStaleUser = true
	got, skip := p.apply([]provider.Message{
		{Role: "user", Content: wrapUserQuery("older question"), Shown: "older question"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: wrapUserQuery("latest question"), Shown: "latest question"},
		{Role: "assistant", Content: "working"},
	}, nil)
	if skip != "" {
		t.Fatal(skip)
	}
	if userTurns(got) != 2 {
		t.Fatalf("stale user was re-appended: %+v", got)
	}
	if visibleUserText(lastUser(got)) != "latest question" {
		t.Fatalf("last user = %q", visibleUserText(lastUser(got)))
	}
}

func TestApplyAllowsReaskingEarlierText(t *testing.T) {
	p := planTurn(&cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
		UserMessage: &cursorpb.UserMessage{Text: "older question"},
	}}})
	got, skip := p.apply([]provider.Message{
		{Role: "user", Content: wrapUserQuery("older question"), Shown: "older question"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: wrapUserQuery("latest question"), Shown: "latest question"},
		{Role: "assistant", Content: "working"},
	}, nil)
	if skip != "" {
		t.Fatal(skip)
	}
	if userTurns(got) != 3 {
		t.Fatalf("intentional re-ask of earlier text must be appended: %+v", got)
	}
	if normalizeUserShown(visibleUserText(lastUser(got))) != "older question" {
		t.Fatalf("last user = %q", visibleUserText(lastUser(got)))
	}
}

func TestApplyAllowsRepeatedSameText(t *testing.T) {
	p := planTurn(&cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
		UserMessage: &cursorpb.UserMessage{Text: "continue"},
	}}})
	got, skip := p.apply([]provider.Message{
		{Role: "user", Content: wrapUserQuery("continue"), Shown: "continue"},
		{Role: "assistant", Content: "paused"},
	}, nil)
	if skip != "" {
		t.Fatal(skip)
	}
	if userTurns(got) != 2 {
		t.Fatalf("repeated continue should still be appended: %+v", got)
	}
}

func wrapUserQuery(shown string) string {
	return "<user_query>\n" + shown + "\n</user_query>"
}

func lastUser(messages []provider.Message) provider.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i]
		}
	}
	return provider.Message{}
}

func TestStaleQueuedUserKeepsNewerHistory(t *testing.T) {
	history := NewHistory(t.TempDir())
	prefix := []provider.Message{
		{Role: "user", Content: wrapUserQuery("first"), Shown: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: wrapUserQuery("older question"), Shown: "older question"},
		{Role: "assistant", Content: "ack"},
	}
	if err := history.Save("conv-stale", prefix); err != nil {
		t.Fatal(err)
	}
	state := protoCloneState(t, history.LoadState("conv-stale"))
	full := append(append([]provider.Message{}, prefix...), provider.Message{Role: "user", Content: wrapUserQuery("latest question"), Shown: "latest question"}, provider.Message{Role: "assistant", Content: "working"}, provider.Message{Role: "tool", Content: "done", ToolCallID: "c1"})
	if err := history.Save("conv-stale", full); err != nil {
		t.Fatal(err)
	}
	loaded := history.Load("conv-stale")
	if visibleUserText(lastUser(loaded)) != "latest question" && !containsRoleText(loaded, "user", "latest question") {
		t.Fatalf("disk history missing latest after save: n=%d %+v", len(loaded), loaded)
	}
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"answering"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	msg := runRequest("mine", "conv-stale", "older question", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().ConversationState = state
	if _, err := hub.Bidi(bidi(t, "stale", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "stale", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		return contextReply(exec)
	})
	if len(fm.requests) == 0 {
		t.Fatal("model was not called")
	}
	body := fm.requests[0]
	if !strings.Contains(body, "latest question") {
		t.Fatalf("latest user was dropped: %s", body)
	}
	idxOld := strings.LastIndex(body, "older question")
	idxNew := strings.LastIndex(body, "latest question")
	if idxNew < 0 || idxOld > idxNew {
		t.Fatalf("model was asked to answer the older user message: %s", body)
	}
	saved := history.Load("conv-stale")
	if visibleUserText(lastUser(saved)) == "older question" {
		t.Fatalf("saved history rolled back to the older user: %+v", saved)
	}
}

func containsRoleText(messages []provider.Message, role, shown string) bool {
	for _, message := range messages {
		if message.Role == role && (visibleUserText(message) == shown || strings.Contains(message.Content, shown)) {
			return true
		}
	}
	return false
}

func contextReply(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
	if exec != nil && exec.GetRequestContextArgs() != nil {
		return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{
			Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}}}},
		}}}
	}
	return &cursorpb.ExecClientMessage{}
}

// Cancel a turn that already applied a newer user, then a following run arrives
// with the shorter pre-cancel ConversationState and the older UserMessageAction.
func TestCancelThenStaleStateKeepsNewerUser(t *testing.T) {
	hold := make(chan struct{})
	fm := &fakeModel{
		responses: []string{
			sse(`{"choices":[{"delta":{"content":"answered first"}}]}`),
			sse(),
			sse(`{"choices":[{"delta":{"content":"should answer latest"}}]}`),
		},
		pause: nil,
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	dir := t.TempDir()
	history := NewHistory(dir)
	hub := New(func(string) (config.Model, bool) { return model, true }, history)

	if _, err := hub.Bidi(bidi(t, "run-a", runRequest("mine", "conv-cancel-stale", "older question", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "run-a", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return contextReply(exec) })
	hub.Done("run-a")
	staleState := protoCloneState(t, history.LoadState("conv-cancel-stale"))

	fm.mu.Lock()
	fm.pause = hold
	fm.mu.Unlock()
	if _, err := hub.Bidi(bidi(t, "run-b", runRequest("mine", "conv-cancel-stale", "latest question", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := hub.Wait(ctx, "run-b")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil })
	}()
	select {
	case <-hold:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start on the newer user")
	}
	cancel()
	hold <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled run did not return")
	}
	fm.mu.Lock()
	fm.pause = nil
	fm.mu.Unlock()
	hub.Done("run-b")

	closed := history.Load("conv-cancel-stale")
	if visibleUserText(lastUser(closed)) != "latest question" {
		t.Fatalf("cancelled turn must keep the newer user: %+v", closed)
	}

	msg := runRequest("mine", "conv-cancel-stale", "older question", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().ConversationState = staleState
	if _, err := hub.Bidi(bidi(t, "run-stale", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "run-stale", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return contextReply(exec) })
	fm.mu.Lock()
	body := fm.requests[len(fm.requests)-1]
	fm.mu.Unlock()
	if !strings.Contains(body, "latest question") {
		t.Fatalf("newer user missing after stale restore: %s", body)
	}
	if strings.LastIndex(body, "older question") > strings.LastIndex(body, "latest question") {
		t.Fatalf("stale older question became the latest user: %s", body)
	}
}

func protoCloneState(t *testing.T, state *cursorpb.ConversationStateStructure) *cursorpb.ConversationStateStructure {
	t.Helper()
	if state == nil {
		t.Fatal("missing state")
	}
	return proto.Clone(state).(*cursorpb.ConversationStateStructure)
}

func TestToolCompletionResumeKeepsNewerHistory(t *testing.T) {
	history := NewHistory(t.TempDir())
	prefix := []provider.Message{
		{Role: "user", Content: wrapUserQuery("older question"), Shown: "older question"},
		{Role: "assistant", Content: "ack"},
	}
	if err := history.Save("conv-resume-stale", prefix); err != nil {
		t.Fatal(err)
	}
	state := protoCloneState(t, history.LoadState("conv-resume-stale"))
	full := append(append([]provider.Message{}, prefix...), provider.Message{Role: "user", Content: wrapUserQuery("latest question"), Shown: "latest question"}, provider.Message{Role: "assistant", Content: "working"}, provider.Message{Role: "tool", Content: "done", ToolCallID: "c1"})
	if err := history.Save("conv-resume-stale", full); err != nil {
		t.Fatal(err)
	}
	if !containsRoleText(history.Load("conv-resume-stale"), "user", "latest question") {
		t.Fatalf("disk history missing latest after save: %+v", history.Load("conv-resume-stale"))
	}
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"continued"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	conversation := "conv-resume-stale"
	detail := "tool finished"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId:    &conversation,
		RequestedModel:    &cursorpb.RequestedModel{ModelId: "mine[context=200k,reasoning=high,fast=false]"},
		ConversationState: state,
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_BackgroundTaskCompletionAction{BackgroundTaskCompletionAction: &cursorpb.BackgroundTaskCompletionAction{
			Completions: []*cursorpb.BackgroundTaskCompletion{{
				TaskId: "t1",
				Kind:   cursorpb.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
				Status: cursorpb.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
				Reason: cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
				Detail: &detail,
			}},
		}}},
	}}}
	if _, err := hub.Bidi(bidi(t, "resume-stale", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "resume-stale", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return contextReply(exec) })
	if len(fm.requests) == 0 {
		t.Fatal("model was not called")
	}
	body := fm.requests[0]
	if !strings.Contains(body, "latest question") {
		t.Fatalf("resume dropped the newer user: %s", body)
	}
	if !strings.Contains(body, "tool finished") {
		t.Fatalf("tool completion input missing: %s", body)
	}
	if strings.Count(body, "older question") > 1 {
		t.Fatalf("older user was replayed: %s", body)
	}
}

func TestQueueWhileTurnRunningKeepsLatestUser(t *testing.T) {
	hold := make(chan struct{})
	fm := &fakeModel{
		responses: []string{
			sse(`{"choices":[{"delta":{"content":"first"}}]}`),
			sse(`{"choices":[{"delta":{"content":"second"}}]}`),
		},
		pause: hold,
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-q", runRequest("mine", "conv-queue", "first instruction", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-q")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil })
	}()
	select {
	case <-hold:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	queued := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
		Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
			UserMessage: &cursorpb.UserMessage{Text: "second instruction"},
		}},
	}}}
	if _, err := hub.Bidi(bidi(t, "run-q", queued)); err != nil {
		t.Fatal(err)
	}
	fm.mu.Lock()
	fm.pause = nil
	fm.mu.Unlock()
	hold <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued turn did not finish")
	}
	fm.mu.Lock()
	requests := append([]string{}, fm.requests...)
	fm.mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("model calls=%d want >=2", len(requests))
	}
	body := requests[len(requests)-1]
	if !strings.Contains(body, "second instruction") {
		t.Fatalf("queued latest user missing: %s", body)
	}
	if strings.LastIndex(body, "first instruction") > strings.LastIndex(body, "second instruction") {
		t.Fatalf("queued later user was not the latest: %s", body)
	}
}

func TestRapidSuccessiveInputsKeepLatest(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"content":"one"}}]}`),
		sse(`{"choices":[{"delta":{"content":"two"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	history := NewHistory(t.TempDir())
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	if _, err := hub.Bidi(bidi(t, "rapid-1", runRequest("mine", "conv-rapid", "first instruction", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "rapid-1", model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil })
	hub.Done("rapid-1")
	if _, err := hub.Bidi(bidi(t, "rapid-2", runRequest("mine", "conv-rapid", "second instruction", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "rapid-2", model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil })
	body := fm.requests[len(fm.requests)-1]
	if strings.LastIndex(body, "first instruction") > strings.LastIndex(body, "second instruction") {
		t.Fatalf("second instruction was not last: %s", body)
	}
	if visibleUserText(lastUser(history.Load("conv-rapid"))) != "second instruction" {
		t.Fatalf("saved last user is not the latest: %+v", history.Load("conv-rapid"))
	}
}

func TestReaskEarlierUserOnCurrentHistoryAppends(t *testing.T) {
	history := NewHistory(t.TempDir())
	full := []provider.Message{
		{Role: "user", Content: wrapUserQuery("older question"), Shown: "older question"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: wrapUserQuery("latest question"), Shown: "latest question"},
		{Role: "assistant", Content: "working"},
	}
	if err := history.Save("conv-reask", full); err != nil {
		t.Fatal(err)
	}
	state := protoCloneState(t, history.LoadState("conv-reask"))
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"answering again"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	msg := runRequest("mine", "conv-reask", "older question", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().ConversationState = state
	if _, err := hub.Bidi(bidi(t, "reask", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "reask", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return contextReply(exec) })
	if len(fm.requests) == 0 {
		t.Fatal("model was not called")
	}
	body := fm.requests[0]
	if !strings.Contains(body, "latest question") {
		t.Fatalf("current later user missing: %s", body)
	}
	if strings.LastIndex(body, "older question") < strings.LastIndex(body, "latest question") {
		t.Fatalf("intentional re-ask did not become the latest user: %s", body)
	}
}
