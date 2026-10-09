package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"

	"google.golang.org/protobuf/proto"
)

func TestReadImageSurvivesTheCheckpoint(t *testing.T) {
	png := []byte{1, 2, 3, 4}
	messages := []provider.Message{
		{Role: "user", Content: "look"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "Read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", Content: "Read image file: /w/a.png", Images: []provider.Image{{MIME: "image/png", Data: png}}},
	}
	built, err := writeCheckpoint("system", "req", messages, 0, 0, 0, "", 0, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range built.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	got, err := restoreMessages(built.State, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Content != "Read image file: /w/a.png" || len(got[2].Images) != 1 || string(got[2].Images[0].Data) != string(png) || got[2].Images[0].MIME != "image/png" {
		t.Fatalf("%+v", got)
	}
}

func TestTurnBlobIsACursorTurn(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "第一句问了什么"},
		{Role: "assistant", Content: "记下了", Reasoning: "think", ReasoningSignature: "sig"},
		{Role: "user", Content: "第二句"},
		{Role: "assistant", Content: "后面的"},
	}
	built, err := writeCheckpoint("system", "req", messages, 0, 200, 10, "", 0, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range built.Blobs {
		if string(blobID(blob.Data)) != string(blob.ID) || len(blob.ID) != 32 {
			t.Fatalf("id len %d", len(blob.ID))
		}
		blobs[string(blob.ID)] = blob.Data
	}
	if len(built.State.GetTurns()) != 2 {
		t.Fatalf("turns %d", len(built.State.GetTurns()))
	}
	var wrapper cursorpb.ConversationTurnStructure
	if err := proto.Unmarshal(blobs[string(built.State.GetTurns()[0])], &wrapper); err != nil {
		t.Fatal(err)
	}
	userID := wrapper.GetAgentConversationTurn().GetUserMessage()
	var user cursorpb.UserMessage
	if err := proto.Unmarshal(blobs[string(userID)], &user); err != nil {
		t.Fatal(err)
	}
	if user.GetText() != "第一句问了什么" {
		t.Fatalf("%q", user.GetText())
	}
	got, err := restoreMessages(built.State, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[1].ReasoningSignature != "sig" || !strings.Contains(got[0].Content, "第一句") {
		t.Fatalf("%+v", got)
	}
	short := proto.Clone(built.State).(*cursorpb.ConversationStateStructure)
	short.RootPromptMessagesJson = short.RootPromptMessagesJson[:3]
	trimmed, err := restoreMessages(short, blobs)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, message := range trimmed {
		text += message.Content
	}
	if strings.Contains(text, "第二句") || strings.Contains(text, "后面的") || !strings.Contains(text, "第一句") {
		t.Fatalf("%s", text)
	}
	if _, err := importPrefetched([]*cursorpb.PreFetchedBlob{{Id: []byte("nope"), Value: []byte("data")}}); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestSavedTurnIsNotMissing(t *testing.T) {
	h := NewHistory(t.TempDir())
	if err := h.Save("conv-re", []provider.Message{
		{Role: "user", Content: "keep me"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "junk later"},
	}); err != nil {
		t.Fatal(err)
	}
	state := h.LoadState("conv-re")
	state.Turns = state.Turns[:1]
	state.RootPromptMessagesJson = nil
	missing := missingBlobs(state, func(id []byte) ([]byte, bool) { return h.GetBlob(id) })
	if len(missing) != 0 {
		t.Fatalf("missing %d", len(missing))
	}
}

func TestShorterTurnsDropTheLaterUser(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "keep me"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "junk later"},
	}
	built, err := writeCheckpoint("", "req", messages, 0, 0, 0, "", 0, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range built.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	state := proto.Clone(built.State).(*cursorpb.ConversationStateStructure)
	state.RootPromptMessagesJson = state.RootPromptMessagesJson[:2]
	got, err := restoreMessages(state, blobs)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, message := range got {
		text += message.Content
	}
	if strings.Contains(text, "junk later") || !strings.Contains(text, "keep me") {
		t.Fatalf("%s", text)
	}
}

func TestReadStepIsNotAShellCommand(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "Read", Arguments: `{"path":"hello.txt"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: "hello inline"},
	}
	built, err := writeCheckpoint("", "req", messages, 0, 0, 0, "", 0, nil, nil, nil)
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
	var sawRead bool
	for _, id := range wrapper.GetAgentConversationTurn().GetSteps() {
		var step cursorpb.ConversationStep
		if err := proto.Unmarshal(blobs[string(id)], &step); err != nil {
			t.Fatal(err)
		}
		call := step.GetToolCall()
		if call == nil {
			continue
		}
		if call.GetShellToolCall() != nil {
			t.Fatalf("tool stored as shell: %s", call.GetShellToolCall().GetArgs().GetCommand())
		}
		if call.GetReadToolCall().GetArgs().GetPath() == "hello.txt" {
			sawRead = true
		}
	}
	if !sawRead {
		t.Fatal("missing Read card")
	}
	rootText := string(blobs[string(built.State.GetRootPromptMessagesJson()[1])])
	if !strings.Contains(rootText, `"type":"tool-call"`) || strings.Contains(rootText, "ShellToolCall") {
		t.Fatalf("%s", rootText)
	}
}

func TestOfficialRootRoundTripsTheTool(t *testing.T) {
	raw := []byte(`{"role":"assistant","id":"1","content":[{"type":"tool-call","toolCallId":"c1","toolName":"Read","args":{"path":"hello.txt"}}]}`)
	id := blobID(raw)
	result := []byte(`{"role":"tool","id":"c1","content":[{"type":"tool-result","toolCallId":"c1","toolName":"Read","result":"hello inline"}]}`)
	resultID := blobID(result)
	state := &cursorpb.ConversationStateStructure{RootPromptMessagesJson: [][]byte{id, resultID}}
	blobs := map[string][]byte{string(id): raw, string(resultID): result}
	got, err := restoreMessages(state, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].Name != "Read" || got[1].Content != "hello inline" {
		t.Fatalf("%+v", got)
	}
	built, err := writeCheckpoint("", "req", got, 0, 0, 0, "", 0, nil, state, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if string(built.State.GetRootPromptMessagesJson()[0]) != string(id) {
		t.Fatal("official root was rewritten")
	}
}

func TestSecondCheckpointKeepsThePrefix(t *testing.T) {
	first := []provider.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two", ReasoningSignature: "sig"}}
	second := append(append([]provider.Message{}, first...), provider.Message{Role: "user", Content: "three"})
	a, err := writeCheckpoint("system", "req", first, 0, 0, 0, "", 0, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range a.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	b, err := writeCheckpoint("system", "req", second, 0, 0, 0, "", 0, nil, a.State, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.State.GetRootPromptMessagesJson()) != len(a.State.GetRootPromptMessagesJson())+1 {
		t.Fatalf("roots %d", len(b.State.GetRootPromptMessagesJson()))
	}
	for i, id := range a.State.GetRootPromptMessagesJson() {
		if string(b.State.GetRootPromptMessagesJson()[i]) != string(id) {
			t.Fatalf("root %d changed", i)
		}
	}
}

func TestChangedSystemKeepsMessageRoots(t *testing.T) {
	messages := []provider.Message{{Role: "user", Content: "one"}}
	a, err := writeCheckpoint("alpha", "req", messages, 0, 0, 0, "", 0, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, blob := range a.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	b, err := writeCheckpoint("beta", "req", messages, 0, 0, 0, "", 0, nil, a.State, blobs)
	if err != nil {
		t.Fatal(err)
	}
	if string(b.State.GetRootPromptMessagesJson()[0]) == string(a.State.GetRootPromptMessagesJson()[0]) {
		t.Fatal("system root stayed")
	}
	if string(b.State.GetRootPromptMessagesJson()[1]) != string(a.State.GetRootPromptMessagesJson()[1]) {
		t.Fatal("message root changed")
	}
}
