package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
)

// ????????????????Cursor ????? Skipped?????????????????
// ?????? 10 ?????????? Taking longer than expected?
func TestWaitReplyBreaksOnNewUserMessage(t *testing.T) {
	s := newSession("r1", config.Model{ID: "mine"}, &cursorpb.AgentRunRequest{}, nil)
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.enqueue(steer{kind: "break", user: &cursorpb.UserMessage{Text: "hi"}})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.waitReply(ctx, 7)
	if !errors.Is(err, errBreak) {
		t.Fatalf("err=%v, want errBreak", err)
	}
	if len(s.pending) != 1 {
		t.Fatalf("pending=%d, want the new message kept for the next model call", len(s.pending))
	}
}
