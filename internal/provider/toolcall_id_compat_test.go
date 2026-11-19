package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestCompatToolCallIDKeepsShortAndHashesLong(t *testing.T) {
	if got := compatToolCallID("call_1"); got != "call_1" {
		t.Fatalf("short id changed: %q", got)
	}
	if got := compatToolCallID(""); got != "" {
		t.Fatalf("empty id changed: %q", got)
	}
	long := strings.Repeat("a", 85)
	got := compatToolCallID(long)
	if got == long || len(got) != maxProviderToolCallID {
		t.Fatalf("long id = %q len=%d, want hashed len %d", got, len(got), maxProviderToolCallID)
	}
	sum := sha256.Sum256([]byte(long))
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash mismatch: %q", got)
	}
	if compatToolCallID(long) != got {
		t.Fatal("mapping is not stable")
	}
}

func TestCompatToolCallIDDistinctLongIDsStayDistinct(t *testing.T) {
	a := strings.Repeat("official-tool-id-A", 6)
	b := strings.Repeat("official-tool-id-B", 6)
	if len(a) <= maxProviderToolCallID || len(b) <= maxProviderToolCallID {
		t.Fatalf("fixtures too short")
	}
	ga, gb := compatToolCallID(a), compatToolCallID(b)
	if ga == a || gb == b || ga == gb {
		t.Fatalf("long IDs not distinctly hashed: %q %q", ga, gb)
	}
	if len(ga) != maxProviderToolCallID || len(gb) != maxProviderToolCallID {
		t.Fatalf("hashed len ga=%d gb=%d", len(ga), len(gb))
	}
	if compatToolCallID(a) != ga || compatToolCallID(b) != gb {
		t.Fatal("mapping is not stable")
	}
	msgs := messagesWithCompatToolCallIDs([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: a, Name: "Read"}, {ID: b, Name: "Read"}}},
		{Role: "tool", ToolCallID: a, Content: "ra"},
		{Role: "tool", ToolCallID: b, Content: "rb"},
	})
	if msgs[0].ToolCalls[0].ID != ga || msgs[1].ToolCallID != ga {
		t.Fatalf("call A not aligned with result A")
	}
	if msgs[0].ToolCalls[1].ID != gb || msgs[2].ToolCallID != gb {
		t.Fatalf("call B not aligned with result B")
	}
}

func TestMessagesWithCompatToolCallIDsAlignsCallAndResultWithoutMutating(t *testing.T) {
	long := strings.Repeat("official-tool-id-", 6) // 17*6=102
	if len(long) <= maxProviderToolCallID {
		t.Fatalf("fixture too short: %d", len(long))
	}
	orig := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: long, Name: "Read", Arguments: `{"path":"sample-note.txt"}`}}},
		{Role: "tool", ToolCallID: long, Content: "SAMPLE_TOKEN_ORANGE_47"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "short-id", Name: "Write"}}},
	}
	got := messagesWithCompatToolCallIDs(orig)
	want := compatToolCallID(long)
	if got[0].ToolCalls[0].ID != want || got[1].ToolCallID != want {
		t.Fatalf("call/result IDs not aligned: %q vs %q", got[0].ToolCalls[0].ID, got[1].ToolCallID)
	}
	if got[2].ToolCalls[0].ID != "short-id" {
		t.Fatalf("short id changed: %q", got[2].ToolCalls[0].ID)
	}
	if orig[0].ToolCalls[0].ID != long || orig[1].ToolCallID != long {
		t.Fatal("original history IDs were mutated")
	}
}

func TestResponsesAndChatBodiesUseCompatCallIDs(t *testing.T) {
	long := strings.Repeat("x", 85)
	req := chatRequest{Messages: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: long, Name: "Read", Arguments: `{"path":"a"}`}}},
		{Role: "tool", ToolCallID: long, Content: "ok"},
	}}
	m := config.Model{Model: "test-model"}
	want := compatToolCallID(long)

	raw, err := responsesBody(m, req)
	if err != nil {
		t.Fatalf("responsesBody: %v", err)
	}
	var resp struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("responses json: %v", err)
	}
	var callID, outID string
	for _, it := range resp.Input {
		switch it.Type {
		case "function_call":
			callID = it.CallID
		case "function_call_output":
			outID = it.CallID
		}
	}
	if callID != want || outID != want || len(callID) > maxProviderToolCallID {
		t.Fatalf("responses call_id call=%q out=%q want=%q", callID, outID, want)
	}
	if req.Messages[0].ToolCalls[0].ID != long || req.Messages[1].ToolCallID != long {
		t.Fatal("responsesBody mutated the request messages")
	}

	raw, err = openAIBody(m, req)
	if err != nil {
		t.Fatalf("openAIBody: %v", err)
	}
	var chat struct {
		Messages []struct {
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		t.Fatalf("chat json: %v", err)
	}
	var chatCall, chatResult string
	for _, msg := range chat.Messages {
		if msg.ToolCallID != "" {
			chatResult = msg.ToolCallID
		}
		for _, tc := range msg.ToolCalls {
			chatCall = tc.ID
		}
	}
	if chatCall != want || chatResult != want {
		t.Fatalf("chat ids call=%q result=%q want=%q", chatCall, chatResult, want)
	}
}
