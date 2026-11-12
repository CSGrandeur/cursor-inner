package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"

	"google.golang.org/protobuf/proto"
)

func turnSteps(t *testing.T, blobs map[string][]byte, id []byte) []string {
	t.Helper()
	var w cursorpb.ConversationTurnStructure
	if err := proto.Unmarshal(blobs[string(id)], &w); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, sid := range w.GetAgentConversationTurn().GetSteps() {
		var step cursorpb.ConversationStep
		if err := proto.Unmarshal(blobs[string(sid)], &step); err != nil {
			t.Fatal(err)
		}
		switch step.Message.(type) {
		case *cursorpb.ConversationStep_AssistantMessage:
			kinds = append(kinds, "assistant:"+step.GetAssistantMessage().GetText())
		case *cursorpb.ConversationStep_ToolCall:
			kinds = append(kinds, "tool")
		case *cursorpb.ConversationStep_ThinkingMessage:
			kinds = append(kinds, "thinking")
		}
	}
	return kinds
}

// 历史被重排（压缩、去重）后，旧轮不能按下标错配：有内容的轮必须保留自己的步骤，
// 不能复用空轮把上一轮输出弄丢。
func TestTurnReuseFollowsUserBoundaryNotIndex(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "A"},
		{Role: "assistant", Content: "answer A"},
		{Role: "user", Content: "B"},
		{Role: "assistant", Content: "answer B"},
	}
	a, err := writeCheckpoint("", "req", messages, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, b := range a.Blobs {
		blobs[string(b.ID)] = b.Data
	}
	// 模拟上一份检查点里混进了同用户的空轮（旧 bug 的产物），且顺序与新历史不同。
	empty, err := writeCheckpoint("", "req", []provider.Message{{Role: "user", Content: "A"}}, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range empty.Blobs {
		blobs[string(b.ID)] = b.Data
	}
	prev := &cursorpb.ConversationStateStructure{Turns: [][]byte{empty.State.GetTurns()[0], a.State.GetTurns()[0], a.State.GetTurns()[1]}}
	reordered := []provider.Message{
		{Role: "user", Content: "B"},
		{Role: "assistant", Content: "answer B"},
		{Role: "user", Content: "A"},
		{Role: "assistant", Content: "answer A"},
	}
	b, err := writeCheckpoint("", "req", reordered, 0, 0, 0, "", 0, nil, prev, blobs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b2 := range b.Blobs {
		blobs[string(b2.ID)] = b2.Data
	}
	if len(b.State.GetTurns()) != 2 {
		t.Fatalf("turns %d", len(b.State.GetTurns()))
	}
	first := turnSteps(t, blobs, b.State.GetTurns()[0])
	if len(first) != 1 || first[0] != "assistant:answer B" {
		t.Fatalf("reordered turn lost its content: %v", first)
	}
	second := turnSteps(t, blobs, b.State.GetTurns()[1])
	if len(second) != 1 || second[0] != "assistant:answer A" {
		t.Fatalf("second turn lost its content: %v", second)
	}
}

// 合成不出内置卡的工具结果（MCP、子代理）不能从 steps 里丢掉。
func TestUnknownToolResultsKeepSteps(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "mcp__browser__click", Arguments: `{"ref":"e1"}`},
			{ID: "c2", Name: "Task", Arguments: `{"prompt":"x"}`},
		}},
		{Role: "tool", ToolCallID: "c1", Content: "clicked"},
		{Role: "tool", ToolCallID: "c2", Content: "subagent done"},
		{Role: "assistant", Content: "finished"},
	}
	built, err := writeCheckpoint("", "req", messages, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, b := range built.Blobs {
		blobs[string(b.ID)] = b.Data
	}
	steps := turnSteps(t, blobs, built.State.GetTurns()[0])
	if len(steps) != 3 {
		t.Fatalf("steps %v, want mcp card + fallback text + assistant", steps)
	}
	if steps[0] != "tool" {
		t.Fatalf("mcp result not a card: %v", steps)
	}
	if !strings.Contains(steps[1], "subagent done") {
		t.Fatalf("unknown tool result dropped: %v", steps)
	}
	var w cursorpb.ConversationTurnStructure
	proto.Unmarshal(blobs[string(built.State.GetTurns()[0])], &w)
	var card cursorpb.ConversationStep
	proto.Unmarshal(blobs[string(w.GetAgentConversationTurn().GetSteps()[0])], &card)
	mcp := card.GetToolCall().GetMcpToolCall()
	if mcp == nil || mcp.GetArgs().GetToolName() != "click" || mcp.GetArgs().GetServerIdentifier() != "browser" {
		t.Fatalf("mcp card wrong: %+v", mcp)
	}
	if mcp.GetResult().GetSuccess().GetContent()[0].GetText().GetText() != "clicked" {
		t.Fatal("mcp result text lost")
	}
}
