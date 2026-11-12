package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
)

func TestContentInspectionPausesKeepsCheckpointAndSkipsDuplicateTools(t *testing.T) {
	writeCall := sse(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Write","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"path\":\"/w/kept.go\",\"contents\":\"package kept\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"path\":\"/w/kept.go\",\"contents\":\"package kept\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Write","arguments":"{\"path\":\"/w/kept.go\",\"contents\":\"package kept\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	failed := sse(
		`{"type":"response.output_text.delta","delta":"already wrote the file"}`,
		`{"type":"response.failed","response":{"error":{"code":"server_error","type":"server_error","message":"InternalError.Algo.DataInspectionFailed: Input text data may contain inappropriate content."}}}`,
	)
	cont := sse(
		`{"type":"response.output_text.delta","delta":"continuing after your edit"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	fm := &fakeModel{responses: []string{writeCall, failed, cont}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-responses", BaseURL: srv.URL, APIKey: "k", Model: "qwen4-max-preview"}
	dir := t.TempDir()
	history := NewHistory(dir)
	hub := New(func(string) (config.Model, bool) { return model, true }, history)

	if _, err := hub.Bidi(bidi(t, "insp-1", runRequest("mine", "conv-insp", "write kept.go", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	var writes, reads int
	var texts []string
	var ended, checkpoint bool
	for _, m := range drive(t, hub, "insp-1", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		switch {
		case exec.GetReadArgs() != nil:
			reads++
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_FileNotFound{FileNotFound: &cursorpb.ReadFileNotFound{Path: exec.GetReadArgs().GetPath()}},
			}}}
		case exec.GetWriteArgs() != nil:
			writes++
			if exec.GetWriteArgs().GetPath() != "/w/kept.go" || !strings.Contains(exec.GetWriteArgs().GetFileText(), "package kept") {
				t.Fatalf("write %v", exec.GetWriteArgs())
			}
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
				Result: &cursorpb.WriteResult_Success{Success: &cursorpb.WriteSuccess{Path: exec.GetWriteArgs().GetPath()}},
			}}}
		default:
			t.Fatalf("unexpected exec %v", exec)
			return nil
		}
	}) {
		if tdelta := m.GetInteractionUpdate().GetTextDelta(); tdelta != nil {
			texts = append(texts, tdelta.GetText())
		}
		if m.GetInteractionUpdate().GetTurnEnded() != nil {
			ended = true
		}
		if m.GetConversationCheckpointUpdate() != nil {
			checkpoint = true
		}
	}
	joined := strings.Join(texts, "")
	if reads != 1 || writes != 1 {
		t.Fatalf("first turn reads=%d writes=%d", reads, writes)
	}
	if !ended || !checkpoint {
		t.Fatalf("ended=%v checkpoint=%v texts=%q", ended, checkpoint, joined)
	}
	if !strings.Contains(joined, "already wrote the file") {
		t.Fatalf("partial body lost: %q", joined)
	}
	if !strings.Contains(joined, "本轮已暂停") || !strings.Contains(joined, "内容审核") {
		t.Fatalf("missing pause notice: %q", joined)
	}
	if !strings.Contains(joined, "InternalError.Algo.DataInspectionFailed") {
		t.Fatalf("notice must show nested vendor code, not only envelope: %q", joined)
	}
	if strings.Contains(joined, "代码 server_error") && !strings.Contains(joined, "InternalError.Algo.DataInspectionFailed") {
		t.Fatalf("envelope type must not replace vendor code: %q", joined)
	}
	if !strings.Contains(joined, "已保留") || !strings.Contains(joined, "更换模型") {
		t.Fatalf("notice missing keep-progress / continue hint: %q", joined)
	}
	if strings.Contains(joined, "Operation interrupted") {
		t.Fatalf("must not look like a hard interrupt: %q", joined)
	}
	if strings.Contains(joined, "Finish the work in smaller steps") {
		t.Fatalf("must not treat inspection as length truncation: %q", joined)
	}
	if len(fm.requests) != 2 {
		t.Fatalf("first turn should stop after rejected request, got %d model requests", len(fm.requests))
	}

	saved := history.Load("conv-insp")
	if len(saved) == 0 {
		t.Fatal("history empty")
	}
	var hasTool, hasNotice bool
	for _, msg := range saved {
		if msg.Role == "tool" && msg.ToolCallID == "call_1" && !msg.IsError {
			hasTool = true
		}
		if msg.Role == "assistant" && strings.Contains(msg.Content, "本轮已暂停") && strings.Contains(msg.Content, "内容审核") {
			hasNotice = true
		}
		if strings.Contains(msg.Content, "Finish the work in smaller steps") {
			t.Fatal("history must not auto-continue as truncation")
		}
	}
	if !hasTool || !hasNotice {
		t.Fatalf("checkpoint history tool=%v notice=%v last=%+v", hasTool, hasNotice, saved[len(saved)-1])
	}

	// Next turn: user revises the task. Completed Write must not be executed again unless the model asks.
	if _, err := hub.Bidi(bidi(t, "insp-2", runRequest("mine", "conv-insp", "我改了说明，请继续", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	for _, m := range drive(t, hub, "insp-2", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		if exec.GetWriteArgs() != nil || exec.GetReadArgs() != nil {
			t.Fatal("must not re-execute the completed Write")
		}
		t.Fatalf("unexpected exec on resume %v", exec)
		return nil
	}) {
		_ = m
	}
	if len(fm.requests) != 3 {
		t.Fatalf("expected one resume model request, got %d", len(fm.requests))
	}
	if !strings.Contains(fm.requests[2], "kept.go") && !strings.Contains(fm.requests[2], "package kept") && !strings.Contains(fm.requests[2], "call_1") {
		t.Fatalf("resume request should carry prior tool work, body=%s", fm.requests[2])
	}
}
