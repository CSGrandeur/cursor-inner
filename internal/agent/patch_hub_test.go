package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
)

func toolCallSSE(t *testing.T, id, name string, args map[string]string) string {
	t.Helper()
	rawArgs, _ := json.Marshal(args)
	event, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": id, "function": map[string]any{"name": name, "arguments": string(rawArgs)}},
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return sse(string(event))
}

// 一次 apply_patch 改两个文件：Cursor 看到两张编辑卡片（第二张用派生 id），模型拿到 Codex 格式的摘要，
// 两个文件都记进“改过的文件”，压缩后会提醒模型重读。
func TestMultiFileApplyPatchThroughTheHub(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: /w/a.go\n@@\n-hello\n+world\n*** Add File: /w/b.go\n+package b\n*** End Patch"
	fm := &fakeModel{responses: []string{
		toolCallSSE(t, "call_patch", "apply_patch", map[string]string{"input": patch}),
		sse(`{"choices":[{"delta":{"content":"done"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "patch", runRequest("mine", "conv-patch", "patch both", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	var writes []string
	msgs := drive(t, hub, "patch", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		switch {
		case exec.GetReadArgs() != nil:
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "hello\n"}}},
			}}}
		case exec.GetWriteArgs() != nil:
			w := exec.GetWriteArgs()
			writes = append(writes, w.GetPath()+"="+w.GetFileText()+"@"+w.GetToolCallId())
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
				Result: &cursorpb.WriteResult_Success{Success: &cursorpb.WriteSuccess{Path: w.GetPath()}},
			}}}
		default:
			t.Fatalf("unexpected exec %v", exec)
			return nil
		}
	})
	if got := kinds(msgs); got != "started,exec:read,exec:write,started,exec:write,completed,completed,text,ended" {
		t.Fatalf("sequence %s", got)
	}
	if strings.Join(writes, ";") != "/w/a.go=world\n@call_patch;/w/b.go=package b\n@call_patch_2" {
		t.Fatalf("writes %v", writes)
	}
	second := msgs[3].GetInteractionUpdate().GetToolCallStarted()
	if second.GetCallId() != "call_patch_2" || second.GetToolCall().GetEditToolCall().GetArgs().GetPath() != "/w/b.go" {
		t.Fatalf("second card %v", second)
	}
	var follow struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal([]byte(fm.requests[1]), &follow)
	tool := follow.Messages[len(follow.Messages)-1]
	if tool["role"] != "tool" || tool["content"] != "Success. Updated the following files:\nM /w/a.go\nA /w/b.go" {
		t.Fatalf("patch result not sent back: %v", tool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "patch")
	if err != nil || strings.Join(session.editedPaths, ",") != "/w/a.go,/w/b.go" {
		t.Fatalf("edited paths %v %v", session.editedPaths, err)
	}
}

// StrReplace 由 Cursor 执行（不走本地卡片），以前不进“改过的文件”，压缩后不会提醒重读。
func TestClientEditsAreNotedForCompaction(t *testing.T) {
	fm := &fakeModel{responses: []string{
		toolCallSSE(t, "call_edit", "StrReplace", map[string]string{"path": "/w/a.go", "old_string": "hello", "new_string": "world"}),
		sse(`{"choices":[{"delta":{"content":"done"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "noted", runRequest("mine", "conv-noted", "edit", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "noted", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		if exec.GetReadArgs() != nil {
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "hello"}}},
			}}}
		}
		return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
			Result: &cursorpb.WriteResult_Success{Success: &cursorpb.WriteSuccess{Path: "/w/a.go"}},
		}}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "noted")
	if err != nil || strings.Join(session.editedPaths, ",") != "/w/a.go" {
		t.Fatalf("edited paths %v %v", session.editedPaths, err)
	}
}
