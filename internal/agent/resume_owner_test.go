package agent

import (
	"context"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

// 官方 run 带 X-Original-Request-Id 的 resume 必须原样交给官方：NoteResume 不认、不留本地槽位。
func TestResumeOfOfficialRunIsNotClaimed(t *testing.T) {
	hub := New(func(id string) (config.Model, bool) { return config.Model{}, false }, NewHistory(t.TempDir()))
	route, err := hub.Bidi(bidi(t, "run-o", runRequest("grok-official", "conv-o", "hi", &cursorpb.RequestContextEnv{})))
	if err != nil {
		t.Fatal(err)
	}
	if route.Local {
		t.Fatal("官方模型的 run 不应本地回答")
	}
	if hub.NoteResume("run-o", "run-o2") {
		t.Fatal("官方 run 的 resume 不能被本机接管")
	}
	if hub.Finished("run-o") || hub.Owns("run-o") || hub.Owns("run-o2") {
		t.Fatal("官方 run 不应算作本机会话")
	}
	if hub.NoteResume("never-seen", "x") || hub.Owns("x") {
		t.Fatal("没见过的 run 不能被本机接管")
	}
}

// 本机答完的 run：resume 接回同一个会话，新 request id 归本机。
func TestResumeOfLocalFinishedRunIsClaimed(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-l", runRequest("mine", "conv-l", "say ok", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-l")
	if err != nil || session == nil {
		t.Fatalf("wait: %v", err)
	}
	if hub.NoteResume("run-l", "run-l2") {
		t.Fatal("还没答完的 run 不能当作 resume 回放")
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-l")
	if !hub.NoteResume("run-l", "run-l2") {
		t.Fatal("本机答完的 run 的 resume 应接回本机")
	}
	got, err := hub.Wait(ctx, "run-l2")
	if err != nil || got != session || !hub.Owns("run-l2") {
		t.Fatalf("resume 应拿到原会话: same=%v err=%v", got == session, err)
	}
}
