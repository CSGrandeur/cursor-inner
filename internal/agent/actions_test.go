package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

func act(a any) *cursorpb.ConversationAction {
	switch v := a.(type) {
	case *cursorpb.ResumeAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: v}}
	case *cursorpb.BackgroundTaskCompletionAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_BackgroundTaskCompletionAction{BackgroundTaskCompletionAction: v}}
	case *cursorpb.GoalContinuationAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_GoalContinuationAction{GoalContinuationAction: v}}
	case *cursorpb.SubscriptionNotificationAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_SubscriptionNotificationAction{SubscriptionNotificationAction: v}}
	case *cursorpb.StartPlanAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_StartPlanAction{StartPlanAction: v}}
	case *cursorpb.ExecutePlanAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ExecutePlanAction{ExecutePlanAction: v}}
	case *cursorpb.AsyncAskQuestionCompletionAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_AsyncAskQuestionCompletionAction{AsyncAskQuestionCompletionAction: v}}
	case *cursorpb.InjectContextAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_InjectContextAction{InjectContextAction: v}}
	case *cursorpb.CancelAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_CancelAction{CancelAction: v}}
	case *cursorpb.CancelSubagentAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_CancelSubagentAction{CancelSubagentAction: v}}
	case *cursorpb.BackgroundShellAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_BackgroundShellAction{BackgroundShellAction: v}}
	case *cursorpb.BackgroundSubagentAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_BackgroundSubagentAction{BackgroundSubagentAction: v}}
	case *cursorpb.ShellCommandAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ShellCommandAction{ShellCommandAction: v}}
	case *cursorpb.SummarizeAction:
		return &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_SummarizeAction{SummarizeAction: v}}
	}
	panic("unknown action")
}

var finishedHistory = []provider.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}

func TestResumeEndsWhenLastTurnFinished(t *testing.T) {
	env := &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}}
	p := planTurn(act(&cursorpb.ResumeAction{RequestContext: env}))
	if p.ctx != env || !p.needsModel() {
		t.Fatalf("resume should carry its context: %+v", p)
	}
	out, skip := p.apply(append([]provider.Message{}, finishedHistory...), nil)
	if skip == "" || len(out) != 2 {
		t.Fatalf("finished history should not call the model: %q %d", skip, len(out))
	}
}

func TestResumeContinuesUnfinishedWork(t *testing.T) {
	cases := map[string][]provider.Message{
		"tool":      {{Role: "user"}, {Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "1"}}}, {Role: "tool", ToolCallID: "1"}},
		"toolcall":  {{Role: "user"}, {Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "1"}}}},
		"truncated": {{Role: "user"}, {Role: "assistant", Content: "half", Truncated: true}},
		"user":      {{Role: "user"}},
	}
	for name, msgs := range cases {
		if _, skip := planTurn(act(&cursorpb.ResumeAction{})).apply(msgs, nil); skip != "" {
			t.Fatalf("%s: should continue, got %q", name, skip)
		}
	}
	if _, skip := planTurn(act(&cursorpb.ResumeAction{})).apply(nil, nil); skip == "" {
		t.Fatal("empty history should not call the model")
	}
}

func TestGoalContinuationFollowsResumeRule(t *testing.T) {
	if _, skip := planTurn(act(&cursorpb.GoalContinuationAction{})).apply(append([]provider.Message{}, finishedHistory...), nil); skip == "" {
		t.Fatal("finished goal turn should end")
	}
}

func completion(reason cursorpb.BackgroundTaskCompletionReason) *cursorpb.BackgroundTaskCompletion {
	out := "/tmp/out.txt"
	detail := "exit 0\nall good"
	return &cursorpb.BackgroundTaskCompletion{
		TaskId: "task-1", Kind: cursorpb.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
		Status: cursorpb.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS, Title: "npm test",
		Detail: &detail, OutputPath: &out, Reason: reason,
	}
}

func TestBackgroundCompletionBecomesInput(t *testing.T) {
	buf := captureSlog(t)
	p := planTurn(act(&cursorpb.BackgroundTaskCompletionAction{Completions: []*cursorpb.BackgroundTaskCompletion{
		completion(cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS),
		completion(cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED),
	}}))
	out, skip := p.apply(append([]provider.Message{}, finishedHistory...), nil)
	if skip != "" || len(out) != 3 || out[2].Role != "user" {
		t.Fatalf("%q %+v", skip, out)
	}
	text := out[2].Content
	for _, want := range []string{"<background_task_completions>", "kind: shell", "status: success", "task_id: task-1", "title: npm test", "output_path: /tmp/out.txt", "all good"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Count(text, "task_id:") != 1 {
		t.Fatalf("progress note should be left out: %s", text)
	}
	logs := buf.String()
	if strings.Count(logs, "background_completion") != 2 || !strings.Contains(logs, "reason=task_progress") || !strings.Contains(logs, "task_id=task-1") {
		t.Fatalf("logs: %s", logs)
	}
}

func TestBackgroundProgressOnlyEnds(t *testing.T) {
	p := planTurn(act(&cursorpb.BackgroundTaskCompletionAction{Completions: []*cursorpb.BackgroundTaskCompletion{
		completion(cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS)}}))
	if p.skip == "" || p.needsModel() {
		t.Fatalf("%+v", p)
	}
}

func TestSteerUsesStructuredCompletion(t *testing.T) {
	text := actionText(act(&cursorpb.BackgroundTaskCompletionAction{Completions: []*cursorpb.BackgroundTaskCompletion{
		completion(cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED)}}))
	if !strings.Contains(text, "status: success") || !strings.Contains(text, "output_path: /tmp/out.txt") {
		t.Fatal(text)
	}
}

func TestInputActions(t *testing.T) {
	env := &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}}
	content := "1. do it"
	path := "/plans/p.md"
	cases := []struct {
		name string
		a    *cursorpb.ConversationAction
		want string
		mode cursorpb.AgentMode
	}{
		{"subscription", act(&cursorpb.SubscriptionNotificationAction{RequestContext: env, Notifications: []*cursorpb.UserMessage{{Text: "build broke"}}}), "build broke", 0},
		{"start plan", act(&cursorpb.StartPlanAction{RequestContext: env, UserMessage: &cursorpb.UserMessage{Text: "plan it", Mode: cursorpb.AgentMode_AGENT_MODE_PLAN}}), "plan it", cursorpb.AgentMode_AGENT_MODE_PLAN},
		{"execute plan", act(&cursorpb.ExecutePlanAction{RequestContext: env, PlanFileContent: &content, PlanFilePath: &path, ExecutionMode: cursorpb.AgentMode_AGENT_MODE_AGENT}), "1. do it", cursorpb.AgentMode_AGENT_MODE_AGENT},
		{"inject user", act(&cursorpb.InjectContextAction{Payload: &cursorpb.InjectContextAction_UserContext{UserContext: &cursorpb.UserContextInjection{RequestContext: env, UserMessage: &cursorpb.UserMessage{Text: "also this"}}}}), "also this", 0},
		{"inject system", act(&cursorpb.InjectContextAction{Payload: &cursorpb.InjectContextAction_SystemContext{SystemContext: &cursorpb.SystemContextInjection{Content: "note"}}}), "note", 0},
		{"ask answer", act(&cursorpb.AsyncAskQuestionCompletionAction{OriginalToolCallId: "call-9", Result: &cursorpb.AskQuestionResult{}}), "tool_call_id: call-9", 0},
	}
	for _, c := range cases {
		p := planTurn(c.a)
		out, skip := p.apply(append([]provider.Message{}, finishedHistory...), env)
		if skip != "" || len(out) != 3 || !strings.Contains(out[2].Content, c.want) || p.mode != c.mode {
			t.Fatalf("%s: skip=%q mode=%v %+v", c.name, skip, p.mode, out)
		}
		if c.name != "inject system" && c.name != "ask answer" && p.ctx != env {
			t.Fatalf("%s: context not taken from the action", c.name)
		}
	}
}

func TestActionsThatNeedNoModel(t *testing.T) {
	for _, a := range []*cursorpb.ConversationAction{
		act(&cursorpb.CancelAction{}), act(&cursorpb.CancelSubagentAction{}), act(&cursorpb.BackgroundShellAction{}),
		act(&cursorpb.BackgroundSubagentAction{}), act(&cursorpb.ShellCommandAction{}),
		act(&cursorpb.SubscriptionNotificationAction{}), act(&cursorpb.InjectContextAction{}), nil,
	} {
		p := planTurn(a)
		if p.skip == "" || p.needsModel() {
			t.Fatalf("%s should not call the model", p.kind)
		}
	}
	if p := planTurn(act(&cursorpb.SummarizeAction{})); !p.summarize || p.needsModel() {
		t.Fatal("summarize keeps its own path")
	}
}

func TestResumeOnFinishedTurnEndsWithoutCallingTheModel(t *testing.T) {
	history := NewHistory(t.TempDir())
	if err := history.Save("conv-resume", finishedHistory); err != nil {
		t.Fatal(err)
	}
	fm := &fakeModel{}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	conversation := "conv-resume"
	env := &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}}
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: "mine"},
		Action:         act(&cursorpb.ResumeAction{RequestContext: env}),
	}}}
	if _, err := hub.Bidi(bidi(t, "resume", msg)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "resume")
	if err != nil {
		t.Fatal(err)
	}
	buf := captureSlog(t)
	ended := false
	if err := session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
		if m.GetInteractionUpdate().GetTurnEnded() != nil {
			ended = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ended || len(fm.requests) != 0 {
		t.Fatalf("ended=%v requests=%d", ended, len(fm.requests))
	}
	if !strings.Contains(buf.String(), "turn_skip") || !strings.Contains(buf.String(), "action=ResumeAction") {
		t.Fatalf("logs: %s", buf.String())
	}
}
