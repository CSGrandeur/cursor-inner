package agent

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

// 压缩工具输出时不能把中文从字节中间切开，否则发给模型的是乱码（U+FFFD）。
func TestCompactKeepsUTF8Valid(t *testing.T) {
	body := strings.Repeat("中文输出", 400) // 每字 3 字节，300/200 都不落在字符边界
	msgs := []provider.Message{{Role: "user", Content: "q"}, {Role: "tool", Content: body}}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, provider.Message{Role: "user", Content: "x"})
	}
	out := compactMessages(msgs, 1)
	if out[1].Content == body {
		t.Fatal("expected compaction")
	}
	if !utf8.ValidString(out[1].Content) {
		t.Fatalf("compacted text is not valid UTF-8: %q", out[1].Content[:40])
	}
	if !utf8.ValidString(clip(body, 301)) {
		t.Fatal("clip broke a character")
	}
	if headUTF8("ab", 5) != "ab" || tailUTF8("ab", 5) != "ab" || headUTF8("中", 2) != "" || tailUTF8("中", 2) != "" {
		t.Fatal("edge cases")
	}
}

// 新一轮（新会话，summaryText 为空）写检查点时要沿用上一份的摘要，否则已压缩的对话续聊时摘要被抹掉。
func TestCheckpointCarriesPreviousSummary(t *testing.T) {
	raw, _ := proto.Marshal(&cursorpb.ConversationSummary{Summary: "earlier work"})
	prev := &cursorpb.ConversationStateStructure{Summary: raw, SelfSummaryCount: 2}
	msgs := []provider.Message{{Role: "user", Content: "<summary>\nearlier work\n</summary>"}, {Role: "user", Content: "next"}, {Role: "assistant", Content: "ok"}}
	built, err := writeCheckpoint("sys", "r", msgs, 0, 0, 0, "", 0, nil, prev, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got cursorpb.ConversationSummary
	if err := proto.Unmarshal(built.State.GetSummary(), &got); err != nil || got.GetSummary() != "earlier work" {
		t.Fatalf("summary lost: %q err=%v", got.GetSummary(), err)
	}
	if built.State.GetSelfSummaryCount() != 2 {
		t.Fatalf("count=%d", built.State.GetSelfSummaryCount())
	}
}

// 正文复读被提前收尾时要告诉用户原因和怎么继续，不能只是戛然而止。
func TestTextLoopStopTellsUser(t *testing.T) {
	chunk := `{"choices":[{"delta":{"content":"I will now fix the bug in the file. "}}]}`
	events := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		events = append(events, chunk)
	}
	fm := &fakeModel{responses: []string{sse(events...)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "rep", runRequest("mine", "conv-rep", "go", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "rep")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	var text strings.Builder
	if err := session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
		text.WriteString(m.GetInteractionUpdate().GetTextDelta().GetText())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "repeating the same text") {
		t.Fatalf("no stop notice in: %q", tailUTF8(text.String(), 200))
	}
}
