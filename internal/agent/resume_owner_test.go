package agent

import (
	"context"
	"strings"
	"sync"
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

// resume 的 run request 常常不带 requested_model。不能因为“认不出模型”就把本机答完的 run
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

// 客户端 resume 时常换一个新的 request id（不带 X-Original-Request-Id），动作还是 ResumeAction。
// 这时要按会话找回已经答完的那条 run 回放，不能当成新的一轮。
func TestResumeWithANewRequestIDFindsTheFinishedRun(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-a", runRequest("mine", "conv-new-id", "hi", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-a")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-a")

	conversation := "conv-new-id"
	resume := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	route, err := hub.Bidi(bidi(t, "run-b", resume))
	if err != nil {
		t.Fatal(err)
	}
	if !route.Local {
		t.Fatal("换 id 的 resume 也要留在本机")
	}
	got, err := hub.Wait(ctx, "run-b")
	if err != nil || got != session {
		t.Fatalf("resume 应拿到原会话: same=%v err=%v", got == session, err)
	}

	// 同一会话里带用户消息的请求必须当成新的一轮，不能回放旧 run。
	next := runRequest("mine", conversation, "next question", &cursorpb.RequestContextEnv{})
	if route, err := hub.Bidi(bidi(t, "run-c", next)); err != nil || !route.Local {
		t.Fatalf("用户消息应留在本机: local=%v err=%v", route.Local, err)
	}
	fresh, err := hub.Wait(ctx, "run-c")
	if err != nil || fresh == session {
		t.Fatalf("带用户消息的请求不能被当成 resume: same=%v err=%v", fresh == session, err)
	}
}

// 同一会话已经开了新的一轮时，换 id 的 resume 不能再回放更早那条答完的 run。
func TestResumeWithANewRequestIDSkipsAStaleRun(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	conversation := "conv-stale"
	if _, err := hub.Bidi(bidi(t, "run-a", runRequest("mine", conversation, "hi", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := hub.Wait(ctx, "run-a")
	if err != nil || first == nil {
		t.Fatalf("session=%v err=%v", first, err)
	}
	if err := first.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-a")
	if _, err := hub.Bidi(bidi(t, "run-b", runRequest("mine", conversation, "next", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	resume := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		Action:         &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}},
	}}}
	if _, err := hub.Bidi(bidi(t, "run-c", resume)); err != nil {
		t.Fatal(err)
	}
	got, _ := hub.Wait(ctx, "run-c")
	if got == first {
		t.Fatal("新一轮已经开始，resume 不能回放上一轮答完的 run")
	}
}

// 热替换和自更新等“回合空闲”只看正在跑的回合：答完但槽位还在的会话不算忙。
func TestActiveCountsOnlyRunningTurns(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-a", runRequest("mine", "conv-active", "hi", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-a")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	session.running.Add(1)
	if got := hub.Active(); got != 1 {
		t.Fatalf("正在跑的回合 Active = %d, want 1", got)
	}
	session.running.Add(-1)
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := hub.Active(); got != 0 {
		t.Fatalf("答完但槽位还在 Active = %d, want 0", got)
	}
}

// RunSSE 先到会建空槽卡在 ready 上；随后的 NoteResume 必须叫醒它，不能整槽替换把 Wait 晾在旧 channel。
func TestNoteResumeWakesWaitThatArrivedFirst(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-race", runRequest("mine", "conv-race", "hi", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-race")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-race")

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	var (
		got  *Session
		werr error
		wg   sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, werr = hub.Wait(waitCtx, "run-race-2")
	}()
	// 给 Wait 一点时间先建出空槽。
	time.Sleep(50 * time.Millisecond)
	if !hub.NoteResume("run-race", "run-race-2") {
		t.Fatal("本机答完的 run 的 resume 应接回本机")
	}
	wg.Wait()
	if werr != nil || got != session {
		t.Fatalf("Wait 应被 NoteResume 叫醒并拿到原会话: same=%v err=%v", got == session, werr)
	}
}

// 取消发生在模型还没回出助手消息时，历史会停在上一条用户消息。
// 若不补“已中断”，下一轮新指令紧跟在后面，模型会先想上一条。
func TestCancelBeforeReplyThenNewUserDoesNotLeaveTrailingUser(t *testing.T) {
	hold := make(chan struct{})
	fm := &fakeModel{
		responses: []string{
			// No assistant content: after pause+cancel the handler may still flush this
			// response (reply is taken before pause). Empty body keeps history on the
			// trailing user so CloseDangling can close it. A non-empty first reply races
			// on CI and leaves "should not finish" as the last assistant message.
			sse(),
			sse(`{"choices":[{"delta":{"content":"answered the new one"}}]}`),
		},
		pause: hold,
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	dir := t.TempDir()
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(dir))

	if _, err := hub.Bidi(bidi(t, "run-old", runRequest("mine", "conv-trail", "previous instruction", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := hub.Wait(ctx, "run-old")
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
		t.Fatal("model did not start")
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
	hub.Done("run-old")

	history := NewHistory(dir).Load("conv-trail")
	if len(history) < 2 || history[len(history)-1].Role != "assistant" || history[len(history)-1].Content != "已停止" {
		t.Fatalf("cancelled turn must close trailing user: %+v", history)
	}
	if history[len(history)-2].Role != "user" || !strings.Contains(history[len(history)-2].Content, "previous instruction") {
		t.Fatalf("expected previous user before interrupt: %+v", history)
	}

	if _, err := hub.Bidi(bidi(t, "run-new", runRequest("mine", "conv-trail", "brand new instruction", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	fresh, err := hub.Wait(ctx2, "run-new")
	if err != nil || fresh == nil || fresh == session {
		t.Fatalf("new turn session=%v same=%v err=%v", fresh != nil, fresh == session, err)
	}
	if err := fresh.Run(ctx2, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fm.mu.Lock()
	requests := append([]string{}, fm.requests...)
	fm.mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("model calls=%d want >=2", len(requests))
	}
	body := requests[len(requests)-1]
	if !strings.Contains(body, "brand new instruction") {
		t.Fatalf("new instruction missing from model body: %s", body)
	}
	if !strings.Contains(body, "已停止") {
		t.Fatalf("stopped marker missing between turns: %s", body)
	}
	// The latest user turn must be the new instruction, not a bare re-ask of the previous one.
	prevIdx := strings.LastIndex(body, "previous instruction")
	newIdx := strings.LastIndex(body, "brand new instruction")
	if prevIdx < 0 || newIdx < 0 || newIdx < prevIdx {
		t.Fatalf("new instruction should come after previous in the request")
	}
}

// NoteResume 挂上旧会话后仍要 Bidi/adopt 新动作；否则续跑会把上一轮用户消息再插一遍。
func TestNoteResumeAdoptsNewActionInsteadOfReplayingOldUser(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"content":"first answer"}}]}`),
		sse(`{"choices":[{"delta":{"content":"handled resume input"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "run-nr", runRequest("mine", "conv-nr", "old user text", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-nr")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-nr")

	detail := "task done"
	completions := &cursorpb.BackgroundTaskCompletionAction{Completions: []*cursorpb.BackgroundTaskCompletion{{
		TaskId: "t1",
		Kind:   cursorpb.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
		Status: cursorpb.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
		Reason: cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
		Detail: &detail,
	}}}
	action := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
		Action: &cursorpb.ConversationAction_BackgroundTaskCompletionAction{BackgroundTaskCompletionAction: completions},
	}}}
	if route, err := hub.Bidi(bidi(t, "run-nr", action)); err != nil || !route.Local {
		t.Fatalf("queue completion: local=%v err=%v", route.Local, err)
	}
	if !hub.NoteResume("run-nr", "run-nr-2") {
		t.Fatal("NoteResume should attach finished run")
	}
	conversation := "conv-nr"
	resume := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	if route, err := hub.Bidi(bidi(t, "run-nr-2", resume)); err != nil || !route.Local {
		t.Fatalf("bidi after NoteResume: local=%v err=%v", route.Local, err)
	}
	got, err := hub.Wait(ctx, "run-nr-2")
	if err != nil || got != session {
		t.Fatalf("same session? %v err=%v", got == session, err)
	}
	if !session.HasQueuedInput() {
		t.Fatal("queued completion missing")
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fm.mu.Lock()
	requests := append([]string{}, fm.requests...)
	fm.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model calls=%d want 2", len(requests))
	}
	body := requests[1]
	if !strings.Contains(body, "task done") {
		t.Fatalf("resume input missing: %s", body)
	}
	// Must not re-append the original user turn as a brand-new trailing user_query.
	if strings.Count(body, "old user text") != 1 {
		t.Fatalf("old user text re-inserted: count=%d body=%s", strings.Count(body, "old user text"), body)
	}
}
