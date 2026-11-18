package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

// An unconfirmed execution pauses before another model action can duplicate it.
func TestSilentEditorEndsTheTurn(t *testing.T) {
	prev := toolWaitLimit
	toolWaitLimit = 150 * time.Millisecond
	t.Cleanup(func() { toolWaitLimit = prev })

	call := func(id string) string {
		return sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + id + `","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`)
	}
	fm := &fakeModel{responses: []string{call("c1"), call("c2"), call("c3"), call("c4")}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", DisplayName: "Mine", Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, id == "mine" }, NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{WorkspacePaths: []string{"/w"}, OsVersion: "linux"}
	if _, err := hub.Bidi(bidi(t, "gone", runRequest("mine", "conv-gone", "read a.go", env))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "gone")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 256)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var text strings.Builder
	ended := false
	for {
		select {
		case m := <-out:
			if exec := m.GetExecServerMessage(); exec.GetRequestContextArgs() != nil {
				if _, err := hub.Bidi(bidi(t, "gone", execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{
					Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: env}}},
				}}}))); err != nil {
					t.Fatal(err)
				}
			}
			u := m.GetInteractionUpdate()
			text.WriteString(u.GetTextDelta().GetText())
			if u.GetTurnEnded() != nil {
				ended = true
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			for len(out) > 0 {
				u := (<-out).GetInteractionUpdate()
				text.WriteString(u.GetTextDelta().GetText())
				if u.GetTurnEnded() != nil {
					ended = true
				}
			}
			fm.mu.Lock()
			defer fm.mu.Unlock()
			if len(fm.requests) != 1 {
				t.Fatalf("model requests=%d, want 1", len(fm.requests))
			}
			if !ended || !strings.Contains(text.String(), "no confirmed terminal result") {
				t.Fatalf("ended=%v text=%q", ended, text.String())
			}
			return
		case <-ctx.Done():
			t.Fatal("session did not finish")
		}
	}
}

// Parallel inactivity also pauses without issuing another batch.
func TestSilentEditorEndsTheTurnForParallelTools(t *testing.T) {
	prev := toolWaitLimit
	toolWaitLimit = 150 * time.Millisecond
	t.Cleanup(func() { toolWaitLimit = prev })

	pair := func(p string) string {
		return sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + p + `1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}},{"index":1,"id":"` + p + `2","function":{"name":"Read","arguments":"{\"path\":\"/w/b.go\"}"}}]}}]}`)
	}
	call := func(id string) string {
		return sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + id + `","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`)
	}
	fm := &fakeModel{responses: []string{pair("a"), pair("b"), sse(`{"choices":[{"delta":{"content":"done"}}]}`)}}
	_ = call
	srv := fm.server(t)
	model := config.Model{ID: "mine", DisplayName: "Mine", Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, id == "mine" }, NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{WorkspacePaths: []string{"/w"}, OsVersion: "linux"}
	if _, err := hub.Bidi(bidi(t, "gone2", runRequest("mine", "conv-gone2", "read a.go", env))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "gone2")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 256)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var text strings.Builder
	ended := false
	for {
		select {
		case m := <-out:
			if exec := m.GetExecServerMessage(); exec.GetRequestContextArgs() != nil {
				if _, err := hub.Bidi(bidi(t, "gone2", execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{
					Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: env}}},
				}}}))); err != nil {
					t.Fatal(err)
				}
			}
			u := m.GetInteractionUpdate()
			text.WriteString(u.GetTextDelta().GetText())
			if u.GetTurnEnded() != nil {
				ended = true
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			for len(out) > 0 {
				u := (<-out).GetInteractionUpdate()
				text.WriteString(u.GetTextDelta().GetText())
				if u.GetTurnEnded() != nil {
					ended = true
				}
			}
			fm.mu.Lock()
			defer fm.mu.Unlock()
			if len(fm.requests) != 1 {
				t.Fatalf("model requests=%d, want 1", len(fm.requests))
			}
			if !ended || !strings.Contains(text.String(), "no confirmed terminal result") {
				t.Fatalf("ended=%v text=%q", ended, text.String())
			}
			return
		case <-ctx.Done():
			t.Fatal("session did not finish")
		}
	}
}
