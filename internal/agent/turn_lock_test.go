package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

func completionRun(model, conversation string) *cursorpb.AgentClientMessage {
	detail := "exit 0"
	return &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: model},
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_BackgroundTaskCompletionAction{BackgroundTaskCompletionAction: &cursorpb.BackgroundTaskCompletionAction{
			Completions: []*cursorpb.BackgroundTaskCompletion{{
				TaskId: "task-1",
				Kind:   cursorpb.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
				Status: cursorpb.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
				Reason: cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
				Title:  "download",
				Detail: &detail,
			}},
		}}},
	}}}
}

// 用户取消一轮的同时后台任务完成：客户端立刻用新的 request id 开一轮。
// 新一轮必须等旧一轮把历史存下来再读，否则模型拿着空历史去处理通知。
func TestWakeWaitsForTheTurnStillEnding(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	replies := []string{
		sse(`{"choices":[{"delta":{"content":"started the download"}}]}`),
		sse(`{"choices":[{"delta":{"content":"download handled"}}]}`),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(requests)
		requests = append(requests, string(body))
		mu.Unlock()
		if n == 0 {
			reached <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, replies[min(n, 1)])
	}))
	defer srv.Close()
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	defer letGo()

	if _, err := hub.Bidi(bidi(t, "run-a", runRequest("mine", "conv-race", "start the long download", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	first, err := hub.Wait(ctx, "run-a")
	if err != nil || first == nil {
		t.Fatalf("session=%v err=%v", first, err)
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil })
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("first turn never reached the model")
	}

	if _, err := hub.Bidi(bidi(t, "run-b", completionRun("mine", "conv-race"))); err != nil {
		t.Fatal(err)
	}
	second, err := hub.Wait(ctx, "run-b")
	if err != nil || second == nil || second == first {
		t.Fatalf("wake should get its own session: %v %v", second, err)
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			if exec := m.GetExecServerMessage(); exec.GetRequestContextArgs() != nil {
				_, err := hub.Bidi(bidi(t, "run-b", execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{
					Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}}}},
				}}})))
				return err
			}
			return nil
		})
	}()

	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	early := len(requests)
	mu.Unlock()
	if early != 1 {
		cancel()
		t.Fatalf("the wake called the model while the first turn was still running (%d requests)", early)
	}
	letGo()

	for _, done := range []chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("turn did not finish")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(requests))
	}
	for _, want := range []string{"start the long download", "started the download", "exit 0"} {
		if !strings.Contains(requests[1], want) {
			t.Fatalf("wake request is missing %q", want)
		}
	}
}

func TestLockTurnGivesUpAfterTheWait(t *testing.T) {
	h := NewHistory(t.TempDir())
	release, ok := h.LockTurn(context.Background(), "c", time.Second)
	if !ok {
		t.Fatal("first lock should succeed")
	}
	started := time.Now()
	if _, ok := h.LockTurn(context.Background(), "c", 50*time.Millisecond); ok || time.Since(started) < 50*time.Millisecond {
		t.Fatal("a busy conversation should time out")
	}
	if other, ok := h.LockTurn(context.Background(), "d", time.Second); !ok {
		t.Fatal("another conversation must not wait")
	} else {
		other()
	}
	release()
	release()
	again, ok := h.LockTurn(context.Background(), "c", time.Second)
	if !ok {
		t.Fatal("lock should be free after release")
	}
	again()
}
