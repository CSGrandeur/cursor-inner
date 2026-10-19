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

// resume 的 run request 常常不带 requested_model。不能因为「认不出模型」就把本机答完的 run
// 交给官方：官方会返回 401，客户端弹出的正是 Error resuming chat。
func TestResumeWithoutAModelStaysLocal(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-nm", runRequest("mine", "conv-nm", "hi", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-nm")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-nm")

	conversation := "conv-nm"
	resume := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		Action:         &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}},
	}}}
	route, err := hub.Bidi(bidi(t, "run-nm", resume))
	if err != nil {
		t.Fatal(err)
	}
	if !route.Local {
		t.Fatal("不带模型的 resume 仍要留在本机")
	}
	got, err := hub.Wait(ctx, "run-nm")
	if err != nil || got != session {
		t.Fatalf("resume 应拿到原会话: same=%v err=%v", got == session, err)
	}
}

// 后台任务完成会送到已经答完的 run 上，客户端随后 resume 这条 run 取结果。
// 这一轮必须真的接着跑，只回放旧状态 + turn_ended 客户端会报 couldn't resume。
func TestBackgroundCompletionOnAFinishedRunContinues(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"content":"first answer"}}]}`),
		sse(`{"choices":[{"delta":{"content":"handled the task"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-bg", runRequest("mine", "conv-bg", "run a task", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-bg")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	runOnce := func() []*cursorpb.AgentServerMessage {
		t.Helper()
		var out []*cursorpb.AgentServerMessage
		if err := session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out = append(out, m)
			return nil
		}); err != nil {
			t.Fatalf("run: %v", err)
		}
		return out
	}
	runOnce()
	hub.Done("run-bg")

	detail := "exit 0"
	completions := &cursorpb.BackgroundTaskCompletionAction{Completions: []*cursorpb.BackgroundTaskCompletion{{
		TaskId: "task-1",
		Kind:   cursorpb.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
		Status: cursorpb.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
		Reason: cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
		Title:  "build",
		Detail: &detail,
	}}}
	action := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
		Action: &cursorpb.ConversationAction_BackgroundTaskCompletionAction{BackgroundTaskCompletionAction: completions},
	}}}
	route, err := hub.Bidi(bidi(t, "run-bg", action))
	if err != nil {
		t.Fatal(err)
	}
	if !route.Local {
		t.Fatal("后台任务结果必须留在本机")
	}

	conversation := "conv-bg"
	resume := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	if route, err = hub.Bidi(bidi(t, "run-bg", resume)); err != nil || !route.Local {
		t.Fatalf("resume 应留在本机: local=%v err=%v", route.Local, err)
	}
	got, err := hub.Wait(ctx, "run-bg")
	if err != nil || got != session {
		t.Fatalf("resume 应拿到原会话: same=%v err=%v", got == session, err)
	}
	if !session.HasQueuedInput() {
		t.Fatal("后台任务结果没有留在会话里")
	}
	out := runOnce()

	fm.mu.Lock()
	requests := append([]string{}, fm.requests...)
	fm.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[1], "exit 0") {
		t.Fatal("第二轮请求里没有后台任务的结果")
	}
	var text string
	for _, m := range out {
		if delta := m.GetInteractionUpdate().GetTextDelta(); delta != nil {
			text += delta.GetText()
		}
	}
	if !strings.Contains(text, "handled the task") {
		t.Fatalf("client text = %q", text)
	}
}
