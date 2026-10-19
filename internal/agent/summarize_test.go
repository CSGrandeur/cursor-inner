package agent

import (
	"context"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

func summarizeMessages() []provider.Message {
	return []provider.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
	}
}

func runSummarize(t *testing.T, conversation string, fm *fakeModel) (*Session, []*cursorpb.AgentServerMessage) {
	t.Helper()
	history := NewHistory(t.TempDir())
	if err := history.Save(conversation, summarizeMessages()); err != nil {
		t.Fatal(err)
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: "mine"},
		Action:         &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_SummarizeAction{SummarizeAction: &cursorpb.SummarizeAction{}}},
	}}}
	if _, err := hub.Bidi(bidi(t, "sum-"+conversation, msg)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "sum-"+conversation)
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	var seen []*cursorpb.AgentServerMessage
	err = session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
		seen = append(seen, m)
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return session, seen
}

func summaryHook(seen []*cursorpb.AgentServerMessage) (string, bool) {
	for _, m := range seen {
		if done := m.GetInteractionUpdate().GetSummaryCompleted(); done != nil {
			return done.GetHookMessage(), true
		}
	}
	return "", false
}

// 模型只回了空白时，历史不能被换成「空摘要 + 尾部两条」：早期上下文会全部丢失，
// 检查点里的 summary 也会变成空串。
func TestSummarizeKeepsHistoryWhenTheReplyIsBlank(t *testing.T) {
	conversation := "conv-blank"
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"\n\n"}}]}`)}}
	session, seen := runSummarize(t, conversation, fm)
	if hook, ok := summaryHook(seen); !ok || hook != "" {
		t.Fatalf("summary hook = %q, completed=%v", hook, ok)
	}
	if session.summaryText != "" {
		t.Fatalf("summaryText = %q", session.summaryText)
	}
	for _, m := range seen {
		if m.GetConversationCheckpointUpdate().GetSelfSummaryCount() != 0 {
			t.Fatal("an empty summary must not be counted as a summary")
		}
	}
	saved := session.history.Load(conversation)
	if len(saved) != len(summarizeMessages()) {
		t.Fatalf("history rewritten: %+v", saved)
	}
	for i, want := range summarizeMessages() {
		if saved[i].Content != want.Content {
			t.Fatalf("message %d = %q, want %q", i, saved[i].Content, want.Content)
		}
	}
}

// 上游正常结束但没有内容（provider 返回空回复错误）时，同样保留历史，不把这一轮变成报错。
func TestSummarizeKeepsHistoryWhenTheReplyIsEmpty(t *testing.T) {
	conversation := "conv-empty"
	empty := `{"choices":[{"delta":{}}]}`
	fm := &fakeModel{responses: []string{sse(empty), sse(empty), sse(empty)}}
	session, seen := runSummarize(t, conversation, fm)
	if hook, ok := summaryHook(seen); !ok || hook != "" {
		t.Fatalf("summary hook = %q, completed=%v", hook, ok)
	}
	if saved := session.history.Load(conversation); len(saved) != len(summarizeMessages()) {
		t.Fatalf("history rewritten: %+v", saved)
	}
}
