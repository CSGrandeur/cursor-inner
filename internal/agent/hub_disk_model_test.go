package agent

import (
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
)

func TestSessionFromDiskRestoresModelAndFallbacks(t *testing.T) {
	dir := t.TempDir()
	hist := NewHistory(dir)
	primary := config.Model{ID: "m-primary", DisplayName: "Primary", Type: "openai-chat", BaseURL: "http://127.0.0.1:9/v1", Model: "p1", Fallback: []string{"m-backup"}}
	backup := config.Model{ID: "m-backup", DisplayName: "Backup", Type: "openai-chat", BaseURL: "http://127.0.0.1:9/v1", Model: "b1"}
	lookup := func(id string) (config.Model, bool) {
		switch id {
		case primary.ID:
			return primary, true
		case backup.ID:
			return backup, true
		default:
			return config.Model{}, false
		}
	}

	conv := "conv-disk-model"
	reqID := "req-disk-1"
	if err := hist.SaveState(conv, &cursorpb.ConversationStateStructure{}); err != nil {
		t.Fatal(err)
	}
	if err := hist.SaveRunModel(reqID, conv, primary.ID); err != nil {
		t.Fatal(err)
	}
	if gotConv := hist.LoadRun(reqID); gotConv != conv {
		t.Fatalf("LoadRun = %q, want %q", gotConv, conv)
	}

	hub := New(lookup, hist)
	// 模拟热交接后收到后台任务完成动作（不带 requested_model）。
	actionMsg := &cursorpb.AgentClientMessage{
		Message: &cursorpb.AgentClientMessage_ConversationAction{
			ConversationAction: &cursorpb.ConversationAction{
				Action: &cursorpb.ConversationAction_BackgroundTaskCompletionAction{
					BackgroundTaskCompletionAction: &cursorpb.BackgroundTaskCompletionAction{},
				},
			},
		},
	}
	hub.mu.Lock()
	route, _, _ := hub.applyLocked(reqID, actionMsg)
	sess := hub.finished[reqID].session
	hub.mu.Unlock()

	if !route.Local {
		t.Fatal("expected disk-backed run to stay local")
	}
	if sess == nil || sess.Model.ID != primary.ID || sess.Model.BaseURL != primary.BaseURL {
		t.Fatalf("restored session lost Model: %+v", sess)
	}
	if len(sess.Fallbacks) != 1 || sess.Fallbacks[0].ID != backup.ID {
		t.Fatalf("restored session lost Fallbacks: %+v", sess.Fallbacks)
	}
}
