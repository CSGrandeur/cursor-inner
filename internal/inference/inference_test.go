package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

func pb(t *testing.T, m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sub(num int, inner []byte) []byte { return msg(num, inner) }

func core(role int, extra ...[]byte) []byte {
	b := appendInt(nil, 1, role)
	for _, e := range extra {
		b = append(b, e...)
	}
	return sub(1, b) // InferenceStreamRequest.messages
}

// 一个真实形状的 invoke_model：系统、用户（含图片）、助手（工具调用 + 签名推理）、工具结果、工具定义。
func sampleInvoke(t *testing.T, id string) []byte {
	params, _ := structpb.NewStruct(map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}})
	args, _ := structpb.NewStruct(map[string]any{"path": "a.go"})
	result := structpb.NewStringValue("package main")
	var req []byte
	req = append(req, core(roleSystem, appendStr(nil, 2, "You are an agent."))...)
	img := sub(2, appendStr(appendStr(nil, 1, "aGVsbG8="), 2, "image/png"))
	textPart := sub(1, appendStr(nil, 1, "read a.go"))
	req = append(req, core(roleUser, sub(3, append(sub(1, textPart), sub(1, img)...)))...)
	call := appendStr(appendStr(nil, 1, "call_1"), 2, "Read")
	call = append(call, sub(3, pb(t, args))...)
	reasoning := appendStr(appendStr(appendStr(nil, 2, "thinking"), 3, "sig"), 5, "openai|https://x|m")
	req = append(req, core(roleAssistant, appendStr(nil, 2, "ok"), sub(4, call), sub(7, reasoning))...)
	resPart := appendStr(appendStr(nil, 1, "call_1"), 2, "Read")
	resPart = append(resPart, sub(3, pb(t, result))...)
	req = append(req, core(roleTool, sub(6, sub(1, resPart)))...)
	tool := appendStr(appendStr(nil, 1, "Read"), 2, "Read a file")
	tool = append(tool, sub(3, pb(t, params))...)
	req = append(req, sub(2, tool)...)
	inv := appendStr(nil, 1, id)
	inv = append(inv, sub(2, req)...)
	return sub(ClientInvokeModel, inv)
}

func TestDecodeInvokeModel(t *testing.T) {
	m, err := DecodeClient(sampleInvoke(t, "inv-1"))
	if err != nil || m.Kind != ClientInvokeModel {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	inv := m.Invoke
	if inv.ID != "inv-1" || inv.System != "You are an agent." || inv.Unsupported != "" {
		t.Fatalf("%+v", inv)
	}
	if len(inv.Messages) != 3 {
		t.Fatalf("messages=%+v", inv.Messages)
	}
	u, a, tr := inv.Messages[0], inv.Messages[1], inv.Messages[2]
	if u.Role != "user" || u.Content != "read a.go" || len(u.Images) != 1 || string(u.Images[0].Data) != "hello" {
		t.Fatalf("user=%+v", u)
	}
	if a.Role != "assistant" || a.Content != "ok" || len(a.ToolCalls) != 1 || a.ToolCalls[0].Name != "Read" || a.Reasoning != "thinking" || a.ReasoningSignature != "sig" || a.ReasoningBy != "openai|https://x|m" {
		t.Fatalf("assistant=%+v", a)
	}
	var args map[string]any
	if json.Unmarshal([]byte(a.ToolCalls[0].Arguments), &args) != nil || args["path"] != "a.go" {
		t.Fatalf("args=%s", a.ToolCalls[0].Arguments)
	}
	if tr.Role != "tool" || tr.ToolCallID != "call_1" || tr.Content != "package main" {
		t.Fatalf("tool=%+v", tr)
	}
	if len(inv.Tools) != 1 || inv.Tools[0].Name != "Read" || !bytes.Contains(inv.Tools[0].Parameters, []byte(`"path"`)) {
		t.Fatalf("tools=%+v", inv.Tools)
	}
}

func TestDecodeRunRequestAndRejectsOthers(t *testing.T) {
	model := appendBool(appendStr(nil, 1, "62c2ab93e935104a"), 2, true)
	rr := appendStr(nil, 1, "conv")
	rr = append(rr, sub(3, model)...)
	rr = appendBool(rr, 18, true)
	m, err := DecodeClient(sub(ClientRunRequest, rr))
	if err != nil || m.Run.ModelID != "62c2ab93e935104a" || !m.Run.MaxMode || m.Run.ConversationID != "conv" {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	if _, err := DecodeClient(sub(ClientRunRequest, appendStr(nil, 1, "conv"))); err == nil {
		t.Fatal("run_request without model must not decode")
	}
	if _, err := DecodeClient([]byte{0x0a, 0x05, 0x01}); err == nil {
		t.Fatal("garbage must not decode")
	}
}

// ---- 会话 ----

type frameSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
	ch  chan struct{}
}

func (f *frameSink) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.buf.Write(b)
	select {
	case f.ch <- struct{}{}:
	default:
	}
	return n, err
}

type serverMsg struct {
	kind   int // 1 heartbeat 2 run_ready 3 response 4 end ; 0 = end-stream
	inv    string
	resp   []field // InferenceStreamResponse fields
	errOut []field
}

func (f *frameSink) messages(t *testing.T) []serverMsg {
	f.mu.Lock()
	b := append([]byte(nil), f.buf.Bytes()...)
	f.mu.Unlock()
	var out []serverMsg
	for len(b) >= 5 {
		n := int(b[1])<<24 | int(b[2])<<16 | int(b[3])<<8 | int(b[4])
		if len(b) < 5+n {
			break
		}
		flags, payload := b[0], b[5:5+n]
		b = b[5+n:]
		if flags&0x02 != 0 {
			out = append(out, serverMsg{kind: 0})
			continue
		}
		fs, err := fields(payload)
		if err != nil || len(fs) != 1 {
			t.Fatalf("bad server message %x", payload)
		}
		inner, _ := fields(fs[0].b)
		sm := serverMsg{kind: fs[0].num, inv: str(inner, 1)}
		if sm.kind == 3 {
			sm.resp, _ = child(inner, 2)
		}
		if sm.kind == 4 {
			sm.errOut, _ = child(inner, 2)
		}
		out = append(out, sm)
	}
	return out
}

func (f *frameSink) waitFor(t *testing.T, pred func([]serverMsg) bool) []serverMsg {
	deadline := time.After(3 * time.Second)
	for {
		if ms := f.messages(t); pred(ms) {
			return ms
		}
		select {
		case <-f.ch:
		case <-deadline:
			t.Fatalf("timeout; got %+v", f.messages(t))
		}
	}
}

func hasEnd(id string) func([]serverMsg) bool {
	return func(ms []serverMsg) bool {
		for _, m := range ms {
			if m.kind == 4 && m.inv == id {
				return true
			}
		}
		return false
	}
}

func TestServeAnswersInvocationAndFinishes(t *testing.T) {
	var gotSystem string
	var gotMsgs []provider.Message
	var gotTools []provider.Tool
	runs := &Runs{Heartbeat: time.Hour, Chat: func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
		gotSystem, gotMsgs, gotTools = system, messages, tools
		onThinking("hmm")
		onText("Hello")
		return provider.Message{Role: "assistant", Content: "Hello", ToolCalls: []provider.ToolCall{{ID: "c9", Name: "Read", Arguments: `{"path":"b.go"}`}}, PromptTokens: 10, CompletionTokens: 3}, nil
	}}
	run := runs.Start("req-1", RunRequest{ModelID: "mine"}, config.Model{ID: "mine", DisplayName: "Mine", ContextWindow: 128000}, 0, true)
	sink := &frameSink{ch: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- runs.Serve(context.Background(), sink, nil, run) }()

	inv, _ := DecodeClient(sampleInvoke(t, "inv-1"))
	run.Append(inv, 1, true)
	ms := sink.waitFor(t, hasEnd("inv-1"))
	if ms[0].kind != 2 {
		t.Fatalf("first message must be run_ready, got %+v", ms[0])
	}
	var text, thinking, toolArgs string
	var final, info bool
	for _, m := range ms {
		if m.kind == 4 && len(m.errOut) > 0 {
			t.Fatalf("invocation ended with error %+v", m.errOut)
		}
		if m.kind != 3 {
			continue
		}
		if m.inv != "inv-1" {
			t.Fatalf("wrong invocation id %q", m.inv)
		}
		if p, ok := child(m.resp, 1); ok {
			text += str(p, 1)
			final = final || boolean(p, 2)
		}
		if p, ok := child(m.resp, 9); ok {
			thinking += str(p, 1)
		}
		if p, ok := child(m.resp, 2); ok && boolean(p, 4) && str(p, 1) == "c9" && str(p, 2) == "Read" {
			toolArgs = str(p, 3)
		}
		if _, ok := child(m.resp, 4); ok {
			info = true
		}
	}
	if text != "Hello" || thinking != "hmm" || toolArgs != `{"path":"b.go"}` || !final || !info {
		t.Fatalf("text=%q thinking=%q args=%q final=%v info=%v", text, thinking, toolArgs, final, info)
	}
	if gotSystem != "You are an agent." || len(gotMsgs) != 3 || len(gotTools) != 1 {
		t.Fatalf("chat got system=%q msgs=%d tools=%d", gotSystem, len(gotMsgs), len(gotTools))
	}

	run.Append(ClientMessage{Kind: ClientFinishRun}, 2, true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after finish_run")
	}
	if last := sink.messages(t); last[len(last)-1].kind != 0 {
		t.Fatal("stream must end with an end-stream frame")
	}
	if runs.Has("req-1") {
		t.Fatal("finished run must be forgotten")
	}
}

func TestServeCancelAndUnsupported(t *testing.T) {
	started := make(chan struct{})
	runs := &Runs{Heartbeat: time.Hour, Chat: func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
		close(started)
		<-ctx.Done()
		return provider.Message{}, ctx.Err()
	}}
	run := runs.Start("req-2", RunRequest{ModelID: "mine"}, config.Model{ID: "mine"}, 0, false)
	sink := &frameSink{ch: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runs.Serve(ctx, sink, nil, run)
	inv, _ := DecodeClient(sampleInvoke(t, "inv-c"))
	run.Append(inv, 0, false)
	<-started
	run.Append(ClientMessage{Kind: ClientCancelInvocation, CancelID: "inv-c"}, 0, false)
	ms := sink.waitFor(t, hasEnd("inv-c"))
	end := ms[len(ms)-1]
	if code := end.errOut; len(code) == 0 || code[0].v != codeCanceled {
		t.Fatalf("want canceled end, got %+v", end)
	}

	compressed := appendStr(nil, 1, "inv-z")
	compressed = append(compressed, sub(7, appendStr(nil, 3, "zz"))...)
	m, err := DecodeClient(sub(ClientInvokeModel, compressed))
	if err != nil {
		t.Fatal(err)
	}
	run.Append(m, 0, false)
	ms = sink.waitFor(t, hasEnd("inv-z"))
	end = ms[len(ms)-1]
	if len(end.errOut) == 0 || end.errOut[0].v != codeUnimplemented {
		t.Fatalf("want unimplemented end, got %+v", end)
	}
}

func TestAppendOrdersBySeqno(t *testing.T) {
	runs := &Runs{}
	run := runs.Start("req-3", RunRequest{}, config.Model{}, 0, true)
	run.Append(ClientMessage{Kind: ClientFinishRun}, 2, true)
	if q := run.take(); len(q) != 0 {
		t.Fatal("out-of-order message must wait for the gap")
	}
	run.Append(ClientMessage{Kind: ClientCancelInvocation, CancelID: "x"}, 1, true)
	q := run.take()
	if len(q) != 2 || q[0].Kind != ClientCancelInvocation || q[1].Kind != ClientFinishRun {
		t.Fatalf("q=%+v", q)
	}
	_ = protox.EndStream
	_ = protowire.BytesType
}

func TestToolSchemaWithoutTopLevelTypeBecomesObject(t *testing.T) {
	// 实测 Cursor 3.26.13 的形状：{"jsonSchema": {...}}
	params, _ := structpb.NewStruct(map[string]any{"jsonSchema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}}})
	pb, _ := proto.Marshal(params)
	var tool []byte
	tool = protowire.AppendTag(tool, 1, protowire.BytesType)
	tool = protowire.AppendString(tool, "Shell")
	tool = protowire.AppendTag(tool, 3, protowire.BytesType)
	tool = protowire.AppendBytes(tool, pb)
	var req []byte
	req = protowire.AppendTag(req, 2, protowire.BytesType)
	req = protowire.AppendBytes(req, tool)
	var im []byte
	im = protowire.AppendTag(im, 1, protowire.BytesType)
	im = protowire.AppendString(im, "inv-1")
	im = protowire.AppendTag(im, 2, protowire.BytesType)
	im = protowire.AppendBytes(im, req)
	fs, err := fields(im)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := decodeInvoke(fs)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(inv.Tools[0].Parameters, &m); err != nil || m["type"] != "object" || m["properties"].(map[string]any)["command"] == nil {
		t.Fatalf("schema=%s err=%v", inv.Tools[0].Parameters, err)
	}
	if string(objectSchema("x", json.RawMessage(`{"type":"object","properties":{}}`))) != `{"type":"object","properties":{}}` {
		t.Fatal("object schema must pass through untouched")
	}
	if got := objectSchema("x", json.RawMessage(`{"jsonSchema":"{\"type\":\"object\",\"properties\":{\"q\":{}}}"}`)); !strings.Contains(string(got), `"q"`) {
		t.Fatalf("string jsonSchema not unwrapped: %s", got)
	}
}
