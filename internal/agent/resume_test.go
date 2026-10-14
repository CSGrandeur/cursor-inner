package agent

import (
	"context"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

// 回合结束后客户端 resume 同一条 run，必须拿到完整的状态回放（blobs + checkpoint），
// 只回 turn_ended 客户端会报 Error resuming chat 并反复重试。
func TestReplayReturnsFinalState(t *testing.T) {
	fm := &fakeModel{
		responses: []string{
			sse(`{"choices":[{"delta":{"reasoning_content":"think"}}]}`, `{"choices":[{"delta":{"content":"ok"}}]}`),
		},
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))

	if _, err := hub.Bidi(bidi(t, "run-r", runRequest("mine", "conv-r", "say ok", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-r")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !session.Finished() {
		t.Fatal("session should be finished")
	}
	frames, err := session.Replay()
	if err != nil {
		t.Fatal(err)
	}
	blobs, checkpoint := 0, 0
	for _, m := range frames {
		if kv := m.GetKvServerMessage(); kv != nil && kv.GetSetBlobArgs() != nil {
			blobs++
			if data, ok := session.history.GetBlob(kv.GetSetBlobArgs().GetBlobId()); !ok || len(data) == 0 {
				t.Fatalf("replayed blob not in history")
			}
		}
		if m.GetConversationCheckpointUpdate() != nil {
			checkpoint++
			st := m.GetConversationCheckpointUpdate()
			if len(st.GetTurns()) == 0 || len(st.GetRootPromptMessagesJson()) == 0 {
				t.Fatalf("checkpoint incomplete: turns=%d roots=%d", len(st.GetTurns()), len(st.GetRootPromptMessagesJson()))
			}
		}
	}
	if blobs == 0 || checkpoint != 1 {
		t.Fatalf("replay blobs=%d checkpoint=%d, want >0 and 1", blobs, checkpoint)
	}
}
