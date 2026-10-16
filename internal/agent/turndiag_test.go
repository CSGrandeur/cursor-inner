package agent

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func captureSlog(t *testing.T) *bytes.Buffer {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestTurnDiagForNonUserAction(t *testing.T) {
	run := &cursorpb.AgentRunRequest{
		ConversationId: proto.String("ffcb63aa-bbbb"),
		Action:         &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_ResumeAction{ResumeAction: &cursorpb.ResumeAction{}}},
	}
	msgs := []provider.Message{{Role: "user", Content: "secret user text"}, {Role: "assistant", Content: "done"}}
	d := newTurnDiag(run, nil, 0, 21, msgs)
	if d.Action != "ResumeAction" || d.HasUser || d.LastRole != "assistant" || d.Turn != 1 || d.Messages != 2 {
		t.Fatalf("%+v", d)
	}
	joined := strings.Join(d.Fallback, "|")
	for _, want := range []string{"没有用户消息", "没有运行环境", "已完成的助手回复"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fallback missing %q: %s", want, joined)
		}
	}
	buf := captureSlog(t)
	d.log()
	out := buf.String()
	for _, want := range []string{"turn_action", "action=ResumeAction", "has_user=false", "tools=21", "mcp_tools=0", "last_role=assistant", "conversation=ffcb63"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "secret user text") {
		t.Fatal("user content leaked")
	}
}

func TestTurnDiagUserActionHidesText(t *testing.T) {
	run := &cursorpb.AgentRunRequest{Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
		UserMessage: &cursorpb.UserMessage{Text: "private prompt text", Mode: cursorpb.AgentMode_AGENT_MODE_AGENT},
	}}}}
	d := newTurnDiag(run, nil, 0, 19, []provider.Message{{Role: "user"}})
	if d.Action != "UserMessageAction" || !d.HasUser || d.Mode != "AGENT_MODE_AGENT" {
		t.Fatalf("%+v", d)
	}
	buf := captureSlog(t)
	d.log()
	out := buf.String()
	if strings.Contains(out, "private prompt text") {
		t.Fatal("user text leaked")
	}
	if !strings.Contains(out, "user_message.text(len=19)") || !strings.Contains(out, "user_message.mode=AGENT_MODE_AGENT") {
		t.Fatalf("field summary missing: %s", out)
	}
}

func TestTurnDiagNoAction(t *testing.T) {
	if d := newTurnDiag(&cursorpb.AgentRunRequest{}, nil, 0, 0, nil); d.Action != "none" {
		t.Fatalf("%+v", d)
	}
}
