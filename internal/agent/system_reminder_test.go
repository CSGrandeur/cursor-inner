package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"

	"google.golang.org/protobuf/proto"
)

func TestSubagentSystemReminderStaysOffTheTranscript(t *testing.T) {
	const reminder = "You are running as a subagent under a parent agent. Do not spawn additional subagents unless requested by the user or by your instructions. Do not create Cursor Canvas files unless requested by the user or by your instructions."
	const prompt = "ICPC: 地球、计时环、三人共一机、百余国"
	msg := userTurn(&cursorpb.UserMessage{
		Text:                   prompt,
		SubagentSystemReminder: proto.String(reminder),
	}, nil)
	if msg.Shown != prompt {
		t.Fatalf("Shown=%q want prompt only", msg.Shown)
	}
	if strings.Contains(msg.Shown, "system_reminder") || strings.Contains(msg.Shown, reminder) {
		t.Fatalf("Shown leaked reminder: %q", msg.Shown)
	}
	if !strings.Contains(msg.Content, "<system_reminder>\n"+reminder+"\n</system_reminder>") {
		t.Fatalf("model Content missing wrapped reminder: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, "<user_query>\n"+prompt+"\n</user_query>") {
		t.Fatalf("model Content missing user_query: %s", msg.Content)
	}
	if visibleUserText(msg) != prompt {
		t.Fatalf("visibleUserText=%q", visibleUserText(msg))
	}

	built, err := writeCheckpoint("system", "req", []provider.Message{
		msg,
		{Role: "assistant", Content: "ok"},
	}, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range built.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	var wrapper cursorpb.ConversationTurnStructure
	if err := proto.Unmarshal(blobs[string(built.State.GetTurns()[0])], &wrapper); err != nil {
		t.Fatal(err)
	}
	var user cursorpb.UserMessage
	if err := proto.Unmarshal(blobs[string(wrapper.GetAgentConversationTurn().GetUserMessage())], &user); err != nil {
		t.Fatal(err)
	}
	if user.GetText() != prompt {
		t.Fatalf("checkpoint text %q", user.GetText())
	}
	if strings.Contains(user.GetText(), "system_reminder") {
		t.Fatalf("checkpoint text has tags: %q", user.GetText())
	}
	if user.GetSubagentSystemReminder() != reminder {
		t.Fatalf("checkpoint field %q", user.GetSubagentSystemReminder())
	}
}

func TestTaggedReminderInUserTextIsStrippedForDisplay(t *testing.T) {
	raw := "<system_reminder>\nYou are running as a subagent under a parent agent.\n</system_reminder>\n\n做短片分镜"
	msg := userTurn(&cursorpb.UserMessage{Text: raw}, nil)
	if msg.Shown != "做短片分镜" {
		t.Fatalf("Shown=%q", msg.Shown)
	}
	if !strings.Contains(msg.Content, "<system_reminder>") || !strings.Contains(msg.Content, "做短片分镜") {
		t.Fatalf("Content=%s", msg.Content)
	}
	if got := visibleUserText(provider.Message{Role: "user", Content: raw}); got != "做短片分镜" {
		t.Fatalf("visible from raw content %q", got)
	}
	if got := visibleUserText(provider.Message{Role: "user", Content: msg.Content}); got != "做短片分镜" {
		t.Fatalf("visible from harness content %q", got)
	}
}

func TestPrependUsersStripsReminderTags(t *testing.T) {
	messages := prependUsers(nil, []*cursorpb.UserMessage{
		{Text: "<system_reminder>\nold\n</system_reminder>\n\n历史问题"},
		{Text: "干净历史", SubagentSystemReminder: proto.String("field only")},
	})
	if len(messages) != 2 {
		t.Fatalf("len=%d", len(messages))
	}
	if messages[0].Shown != "历史问题" || strings.Contains(messages[0].Shown, "system_reminder") {
		t.Fatalf("shown0=%q", messages[0].Shown)
	}
	if messages[1].Shown != "干净历史" {
		t.Fatalf("shown1=%q", messages[1].Shown)
	}
	if !strings.Contains(messages[1].Content, "<system_reminder>\nfield only\n</system_reminder>") {
		t.Fatalf("content1=%s", messages[1].Content)
	}
}

func TestModelSystemReminderDoesNotDoubleWrap(t *testing.T) {
	already := "<system_reminder>\nside chat note\n</system_reminder>"
	if got := modelSystemReminder(already); got != already {
		t.Fatalf("got %q", got)
	}
	if got := modelSystemReminder("plain"); got != "<system_reminder>\nplain\n</system_reminder>" {
		t.Fatalf("got %q", got)
	}
}

func TestRestoreRootUserStripsReminderForTranscript(t *testing.T) {
	raw, err := json.Marshal(wireDoc{
		Role:    "user",
		ID:      "m0",
		Content: []wirePart{{Type: "text", Text: "<system_reminder>\nYou are running as a subagent under a parent agent.\n</system_reminder>\n\nICPC: 地球、计时环"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, kind, err := decodeRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if kind != rootMessage {
		t.Fatalf("kind=%d", kind)
	}
	if strings.Contains(msg.Shown, "system_reminder") || msg.Shown != "ICPC: 地球、计时环" {
		t.Fatalf("Shown=%q", msg.Shown)
	}
	if !strings.Contains(msg.Content, "<system_reminder>") {
		t.Fatalf("Content lost reminder: %s", msg.Content)
	}

	built, err := writeCheckpoint("system", "req", []provider.Message{
		msg,
		{Role: "assistant", Content: "ok"},
	}, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range built.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	var wrapper cursorpb.ConversationTurnStructure
	if err := proto.Unmarshal(blobs[string(built.State.GetTurns()[0])], &wrapper); err != nil {
		t.Fatal(err)
	}
	var user cursorpb.UserMessage
	if err := proto.Unmarshal(blobs[string(wrapper.GetAgentConversationTurn().GetUserMessage())], &user); err != nil {
		t.Fatal(err)
	}
	if user.GetText() != "ICPC: 地球、计时环" {
		t.Fatalf("checkpoint text %q", user.GetText())
	}
	if user.GetSubagentSystemReminder() == "" || strings.Contains(user.GetSubagentSystemReminder(), "<") {
		t.Fatalf("checkpoint field %q", user.GetSubagentSystemReminder())
	}
}

func TestTranscriptUserMessageKeepsFieldStripsText(t *testing.T) {
	const reminder = "You are running as a subagent under a parent agent."
	in := &cursorpb.UserMessage{
		Text:                   "<system_reminder>\n" + reminder + "\n</system_reminder>\n\n短片分镜",
		SubagentSystemReminder: proto.String(reminder),
	}
	out := transcriptUserMessage(in)
	if out.GetText() != "短片分镜" {
		t.Fatalf("text=%q", out.GetText())
	}
	if out.GetSubagentSystemReminder() != reminder {
		t.Fatalf("field=%q", out.GetSubagentSystemReminder())
	}
	if in.GetText() == out.GetText() && strings.Contains(in.GetText(), "system_reminder") {
		// ensure we did not mutate incorrectly — in still has tags, out does not
	}
	if !strings.Contains(in.GetText(), "system_reminder") {
		t.Fatalf("input should stay unchanged: %q", in.GetText())
	}
}
