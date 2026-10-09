package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

// fakeModel 按顺序返回预设的 SSE 响应，并记录每次收到的请求体。
type fakeModel struct {
	mu        sync.Mutex
	responses []string
	requests  []string
	// pause 非空时，写出响应前先通知测试并等待放行。用来在模型还没返回时插入 steer。
	pause chan struct{}
}

func (f *fakeModel) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, string(body))
		reply := f.responses[0]
		f.responses = f.responses[1:]
		pause := f.pause
		f.mu.Unlock()
		if pause != nil {
			pause <- struct{}{}
			<-pause
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String() + "data: [DONE]\n\n"
}

func bidi(t *testing.T, requestID string, msg *cursorpb.AgentClientMessage) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	body := protox.AppendString(nil, 1, protox.EncodeHex(raw))
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, requestID))
	return protox.Frame(0, body)
}

func runRequest(model, conversation, text string, env *cursorpb.RequestContextEnv) *cursorpb.AgentClientMessage {
	action := &cursorpb.UserMessageAction{UserMessage: &cursorpb.UserMessage{Text: text}}
	if env != nil {
		action.RequestContext = &cursorpb.RequestContext{Env: env}
	}
	return &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: model + "[context=200k,reasoning=high,fast=false]"},
		Action:         &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: action}},
	}}}
}

func execResult(id uint32, result *cursorpb.ExecClientMessage) *cursorpb.AgentClientMessage {
	result.Id = id
	return &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: result}}
}

// drive 运行一次 Session，并扮演 Cursor：收到执行请求时用 answer 的返回值回复。
func drive(t *testing.T, hub *Hub, requestID string, model config.Model, answer func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage) []*cursorpb.AgentServerMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, requestID)
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 64)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var got []*cursorpb.AgentServerMessage
	for {
		select {
		case m := <-out:
			if m.GetInteractionUpdate().GetHeartbeat() != nil {
				continue
			}
			got = append(got, m)
			if exec := m.GetExecServerMessage(); exec != nil {
				if _, err := hub.Bidi(bidi(t, requestID, execResult(exec.GetId(), answer(exec)))); err != nil {
					t.Fatal(err)
				}
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			for len(out) > 0 {
				got = append(got, <-out)
			}
			return got
		case <-ctx.Done():
			t.Fatal("session did not finish")
		}
	}
}

func kinds(msgs []*cursorpb.AgentServerMessage) string {
	var parts []string
	for _, m := range msgs {
		switch {
		case m.GetExecServerMessage().GetReadArgs() != nil:
			parts = append(parts, "exec:read")
		case m.GetExecServerMessage().GetWriteArgs() != nil:
			parts = append(parts, "exec:write")
		case m.GetExecServerMessage().GetDeleteArgs() != nil:
			parts = append(parts, "exec:delete")
		case m.GetExecServerMessage().GetShellStreamArgs() != nil:
			parts = append(parts, "exec:shell")
		case m.GetInteractionUpdate().GetShellOutputDelta() != nil:
			parts = append(parts, "shell-delta")
		case m.GetExecServerMessage().GetRequestContextArgs() != nil:
			parts = append(parts, "exec:context")
		case m.GetInteractionUpdate().GetTextDelta() != nil:
			parts = append(parts, "text")
		case m.GetInteractionUpdate().GetToolCallStarted() != nil:
			parts = append(parts, "started")
		case m.GetInteractionUpdate().GetToolCallCompleted() != nil:
			parts = append(parts, "completed")
		case m.GetConversationCheckpointUpdate() != nil || m.GetKvServerMessage().GetSetBlobArgs() != nil:
			continue
		case m.GetInteractionUpdate().GetTurnEnded() != nil:
			parts = append(parts, "ended")
		default:
			parts = append(parts, "other")
		}
	}
	return strings.Join(parts, ",")
}

func TestToolLoopReadsFileAndKeepsHistory(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"It defines hello."}}]}`),
		sse(`{"choices":[{"delta":{"content":"Yes."}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", DisplayName: "Mine", Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, id == "mine" }, NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{WorkspacePaths: []string{"/w"}, OsVersion: "linux"}

	route, err := hub.Bidi(bidi(t, "r1", runRequest("mine", "conv-1", "what is in a.go?", env)))
	if err != nil || !route.Local || route.ModelID != "mine" {
		t.Fatalf("%+v %v", route, err)
	}
	msgs := drive(t, hub, "r1", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		if exec.GetReadArgs().GetPath() != "/w/a.go" {
			t.Fatalf("unexpected exec %v", exec)
		}
		return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
			Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "func hello() {}"}}},
		}}}
	})
	if got := kinds(msgs); got != "started,exec:read,completed,text,ended" {
		t.Fatalf("sequence %s", got)
	}
	completed := msgs[2].GetInteractionUpdate().GetToolCallCompleted()
	if completed.GetCallId() != "call_1" || completed.GetToolCall().GetReadToolCall().GetResult().GetSuccess().GetContent() != "func hello() {}" {
		t.Fatalf("%v", completed)
	}
	var second struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal([]byte(fm.requests[1]), &second)
	last := second.Messages[len(second.Messages)-1]
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" || last["content"] != "func hello() {}" {
		t.Fatalf("tool result not sent back: %v", last)
	}
	if !strings.Contains(second.Messages[0]["content"].(string), "/w") {
		t.Fatal("system prompt lacks workspace path")
	}

	hub.Done("r1")
	if _, err := hub.Bidi(bidi(t, "r2", runRequest("mine", "conv-1", "sure?", env))); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "r2", model, nil)
	var third struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal([]byte(fm.requests[2]), &third)
	if n := len(third.Messages); n != 6 || !strings.Contains(third.Messages[n-1]["content"].(string), "sure?") || third.Messages[4]["content"] != "It defines hello." {
		t.Fatalf("history not carried over: %d messages", n)
	}
}

func TestEditToolsReplaceWriteAndDelete(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_edit","function":{"name":"StrReplace","arguments":"{\"path\":\"/w/a.go\",\"old_string\":\"hello\",\"new_string\":\"world\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_del","function":{"name":"Delete","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"done"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{OsVersion: "linux"}
	if _, err := hub.Bidi(bidi(t, "edit", runRequest("mine", "conv-edit", "change a.go", env))); err != nil {
		t.Fatal(err)
	}
	msgs := drive(t, hub, "edit", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		switch {
		case exec.GetReadArgs() != nil:
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "hello"}}},
			}}}
		case exec.GetWriteArgs() != nil:
			if exec.GetWriteArgs().GetFileText() != "world" || exec.GetId() == 0 {
				t.Fatalf("write %v", exec.GetWriteArgs())
			}
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
				Result: &cursorpb.WriteResult_Success{Success: &cursorpb.WriteSuccess{Path: "/w/a.go"}},
			}}}
		case exec.GetDeleteArgs() != nil:
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_DeleteResult{DeleteResult: &cursorpb.DeleteResult{
				Result: &cursorpb.DeleteResult_Success{Success: &cursorpb.DeleteSuccess{Path: "/w/a.go"}},
			}}}
		default:
			t.Fatalf("unexpected exec %v", exec)
			return nil
		}
	})
	if got := kinds(msgs); got != "started,exec:read,exec:write,completed,started,exec:delete,completed,text,ended" {
		t.Fatalf("sequence %s", got)
	}
	edited := msgs[3].GetInteractionUpdate().GetToolCallCompleted().GetToolCall().GetEditToolCall().GetResult().GetSuccess()
	if edited.GetAfterFullFileContent() != "world" || edited.GetLinesAdded() != 1 || edited.GetLinesRemoved() != 1 {
		t.Fatalf("%v", edited)
	}
	if msgs[6].GetInteractionUpdate().GetToolCallCompleted().GetToolCall().GetDeleteToolCall().GetResult().GetSuccess().GetPath() != "/w/a.go" {
		t.Fatal(msgs[6])
	}
	var follow struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal([]byte(fm.requests[1]), &follow)
	tool := follow.Messages[len(follow.Messages)-1]
	if tool["role"] != "tool" || !strings.Contains(tool["content"].(string), "write success path=/w/a.go") {
		t.Fatalf("edit result not sent back: %v", tool)
	}
}

func TestParallelReadsKeepResultOrder(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"Read","arguments":"{\"path\":\"/w/a\"}"}},{"index":1,"id":"b","function":{"name":"Read","arguments":"{\"path\":\"/w/b\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"done"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "par", runRequest("mine", "conv-par", "read both", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "par")
	if err != nil || session == nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 64)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var execs []*cursorpb.ExecServerMessage
	for {
		select {
		case m := <-out:
			if exec := m.GetExecServerMessage(); exec != nil {
				execs = append(execs, exec)
				if len(execs) == 2 {
					for i := len(execs) - 1; i >= 0; i-- {
						body := "first"
						if execs[i].GetReadArgs().GetPath() == "/w/b" {
							body = "second"
						}
						if _, err := hub.Bidi(bidi(t, "par", execResult(execs[i].GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
							Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: execs[i].GetReadArgs().GetPath(), Output: &cursorpb.ReadSuccess_Content{Content: body}}},
						}}}))); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			var follow struct {
				Messages []map[string]any `json:"messages"`
			}
			_ = json.Unmarshal([]byte(fm.requests[1]), &follow)
			var tools []string
			for _, message := range follow.Messages {
				if message["role"] == "tool" {
					tools = append(tools, message["content"].(string))
				}
			}
			if len(tools) != 2 || tools[0] != "first" || tools[1] != "second" {
				t.Fatalf("%v", tools)
			}
			return
		case <-ctx.Done():
			t.Fatal("timeout")
		}
	}
}

func TestShellStreamsOutputUntilExit(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"sh","function":{"name":"Shell","arguments":"{\"command\":\"echo hi\",\"working_directory\":\"/w\",\"block_until_ms\":5000}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"said hi"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{TerminalsFolder: "/tmp/terminals"}
	if _, err := hub.Bidi(bidi(t, "sh", runRequest("mine", "conv-sh", "run it", env))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "sh")
	if err != nil || session == nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 64)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var got []*cursorpb.AgentServerMessage
	for {
		select {
		case m := <-out:
			if m.GetInteractionUpdate().GetHeartbeat() != nil {
				continue
			}
			got = append(got, m)
			exec := m.GetExecServerMessage()
			if exec.GetShellStreamArgs() == nil {
				continue
			}
			args := exec.GetShellStreamArgs()
			if args.GetCommand() != "echo hi" || args.GetTimeout() != 5000 || args.GetWorkingDirectory() != "/w" || args.GetConversationId() != "conv-sh" {
				t.Fatalf("%v", args)
			}
			id := exec.GetId()
			send := func(msg *cursorpb.ExecClientMessage) {
				t.Helper()
				if _, err := hub.Bidi(bidi(t, "sh", execResult(id, msg))); err != nil {
					t.Fatal(err)
				}
			}
			send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "hi\n"}}}}})
			send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{Event: &cursorpb.ShellStream_Stderr{Stderr: &cursorpb.ShellStreamStderr{Data: "warn\n"}}}}})
			send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0, Cwd: "/w"}}}}})
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case m := <-out:
					if m.GetInteractionUpdate().GetHeartbeat() == nil {
						got = append(got, m)
					}
				default:
					goto shellChecked
				}
			}
		shellChecked:
			if kinds(got) != "started,exec:shell,shell-delta,shell-delta,completed,text,ended" {
				t.Fatalf("sequence %s", kinds(got))
			}
			success := got[4].GetInteractionUpdate().GetToolCallCompleted().GetToolCall().GetShellToolCall().GetResult().GetSuccess()
			if success.GetStdout() != "hi\n" || success.GetStderr() != "warn\n" || success.GetExitCode() != 0 {
				t.Fatalf("%v", success)
			}
			var follow struct {
				Messages []map[string]any `json:"messages"`
			}
			_ = json.Unmarshal([]byte(fm.requests[1]), &follow)
			last := follow.Messages[len(follow.Messages)-1]
			if last["role"] != "tool" || last["content"] != "hi\n\n\n<stderr>\nwarn\n\n</stderr>" {
				t.Fatalf("%v", last)
			}
			return
		case <-ctx.Done():
			t.Fatal("timeout")
		}
	}
}

func TestCancelAbortsInflightAndClosesHistory(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	dir := t.TempDir()
	hub := New(func(id string) (config.Model, bool) { return model, true }, NewHistory(dir))
	if _, err := hub.Bidi(bidi(t, "stop", runRequest("mine", "conv-stop", "read it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := hub.Wait(ctx, "stop")
	if err != nil || session == nil {
		t.Fatal(err)
	}
	var abortID uint32
	runErr := session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
		if m.GetExecServerMessage().GetReadArgs() != nil {
			cancel()
		}
		if abort := m.GetExecServerControlMessage().GetAbort(); abort != nil {
			abortID = abort.GetId()
		}
		return nil
	})
	if runErr == nil {
		t.Fatal("expected cancel")
	}
	if abortID == 0 {
		t.Fatal("missing abort")
	}
	history := NewHistory(dir).Load("conv-stop")
	var tool, assistant bool
	for _, message := range history {
		if message.Role == "tool" && message.IsError && message.ToolCallID == "call_1" {
			tool = true
		}
		if message.Role == "assistant" && message.Content == "Operation interrupted." {
			assistant = true
		}
	}
	if !tool || !assistant {
		t.Fatalf("history not repaired: %+v", history)
	}
}

func TestSessionRequestsContextWhenMissing(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "r", runRequest("mine", "", "hi", nil))); err != nil {
		t.Fatal(err)
	}
	msgs := drive(t, hub, "r", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{
			Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{WorkspacePaths: []string{"/proj"}}}}},
		}}}
	})
	if got := kinds(msgs); got != "exec:context,text,ended" {
		t.Fatalf("sequence %s", got)
	}
	if !strings.Contains(fm.requests[0], "/proj") {
		t.Fatal("fetched workspace path not in prompt")
	}
}

func TestSelectedContextAndReasoningEffortReachTheModel(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m", Reasoning: true}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	msg := runRequest("mine", "conv", "what is this?", &cursorpb.RequestContextEnv{OsVersion: "linux"})
	msg.GetRunRequest().GetAction().GetUserMessageAction().GetUserMessage().SelectedContext = &cursorpb.SelectedContext{
		Files: []*cursorpb.SelectedFile{{Path: "/w/a.go", Content: "package a"}},
	}
	msg.GetRunRequest().GetAction().GetUserMessageAction().GetUserMessage().Mode = cursorpb.AgentMode_AGENT_MODE_PLAN
	if _, err := hub.Bidi(bidi(t, "sel", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "sel", model, nil)
	body := fm.requests[0]
	if !strings.Contains(body, "package a") || !strings.Contains(body, `"reasoning_effort":"high"`) || !strings.Contains(body, `"Shell"`) {
		t.Fatalf("%s", body)
	}
	if strings.Contains(body, `"Write"`) {
		t.Fatal("plan mode offered Write")
	}
}

func TestAskQuestionWaitsForTheUser(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"ask","function":{"name":"AskQuestion","arguments":"{\"questions\":[{\"id\":\"q1\",\"prompt\":\"Which?\",\"options\":[{\"id\":\"a\",\"label\":\"Alpha\"}]}]}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"You picked Alpha."}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "ask", runRequest("mine", "conv-ask", "ask me", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "ask")
	if err != nil || session == nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var sawQuery bool
	for {
		select {
		case m := <-out:
			if m.GetInteractionUpdate().GetHeartbeat() != nil {
				continue
			}
			query := m.GetInteractionQuery().GetAskQuestionInteractionQuery()
			if query == nil {
				continue
			}
			sawQuery = true
			reply := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_InteractionResponse{InteractionResponse: &cursorpb.InteractionResponse{
				Id: m.GetInteractionQuery().GetId(),
				Result: &cursorpb.InteractionResponse_AskQuestionInteractionResponse{AskQuestionInteractionResponse: &cursorpb.AskQuestionInteractionResponse{
					Result: &cursorpb.AskQuestionResult{Result: &cursorpb.AskQuestionResult_Success{Success: &cursorpb.AskQuestionSuccess{
						Answers: []*cursorpb.AskQuestionSuccess_Answer{{QuestionId: "q1", SelectedOptionIds: []string{"a"}}},
					}}},
				}},
			}}}
			if _, err := hub.Bidi(bidi(t, "ask", reply)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if !sawQuery || !strings.Contains(fm.requests[1], "q1: a") {
				t.Fatalf("query=%v body=%s", sawQuery, fm.requests)
			}
			return
		case <-ctx.Done():
			t.Fatal("ask question did not finish")
		}
	}
}

func TestBreakReplacesTheOpenTool(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"switched"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "br", runRequest("mine", "conv-br", "read it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "br")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var aborted bool
	for {
		select {
		case m := <-out:
			if m.GetExecServerControlMessage().GetAbort() != nil {
				aborted = true
			}
			if m.GetExecServerMessage().GetReadArgs() == nil {
				continue
			}
			action := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
				Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{UserMessage: &cursorpb.UserMessage{Text: "do this instead"}}},
			}}}
			if _, err := hub.Bidi(bidi(t, "br", action)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if !aborted || len(fm.requests) < 2 || !strings.Contains(fm.requests[1], "do this instead") {
				t.Fatalf("aborted=%v requests=%d", aborted, len(fm.requests))
			}
			return
		case <-ctx.Done():
			t.Fatal("break did not finish")
		}
	}
}

func TestSteerKeepsTheOpenTool(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"continued"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "br", runRequest("mine", "conv-br", "read it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "br")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var aborted, queued, delivered bool
	var echoed string
	for {
		select {
		case m := <-out:
			if m.GetExecServerControlMessage().GetAbort() != nil {
				aborted = true
			}
			state := m.GetInteractionUpdate().GetContextInjectionState().GetState()
			if state.GetQueued() != nil {
				queued = true
			}
			if state.GetDelivered() != nil {
				delivered = true
			}
			if appended := m.GetInteractionUpdate().GetUserMessageAppended().GetUserMessage(); appended.GetMessageId() != "" {
				echoed = appended.GetMessageId()
			}
			exec := m.GetExecServerMessage()
			if exec.GetReadArgs() == nil {
				continue
			}
			note := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
				Action: &cursorpb.ConversationAction_InjectContextAction{InjectContextAction: &cursorpb.InjectContextAction{
					InjectionId:   "inj-1",
					ExpectedRunId: "br",
					Payload: &cursorpb.InjectContextAction_UserContext{UserContext: &cursorpb.UserContextInjection{UserMessage: &cursorpb.UserMessage{
						Text: "follow-up", MessageId: "msg-steer",
					}}},
				}},
			}}}
			route, err := hub.Bidi(bidi(t, "other", note))
			if err != nil || !route.Local {
				t.Fatalf("local=%v err=%v", route.Local, err)
			}
			result := execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "package a"}}},
			}}})
			if _, err := hub.Bidi(bidi(t, "br", result)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if aborted || !queued || !delivered || echoed != "msg-steer" || len(fm.requests) < 2 {
				t.Fatalf("aborted=%v queued=%v delivered=%v echoed=%s requests=%d", aborted, queued, delivered, echoed, len(fm.requests))
			}
			body := fm.requests[1]
			if !strings.Contains(body, "follow-up") || !strings.Contains(body, "package a") || strings.Contains(body, "Interrupted") {
				t.Fatalf("%s", body)
			}
			return
		case <-ctx.Done():
			t.Fatal("steer did not finish")
		}
	}
}

func TestSteerSkipsToolsThatHaveNotStarted(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Shell","arguments":"{\"command\":\"echo hi\"}"}},{"index":1,"id":"call_2","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"stopped early"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "sk", runRequest("mine", "conv-sk", "run it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "sk")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var sawRead bool
	for {
		select {
		case m := <-out:
			if m.GetExecServerMessage().GetReadArgs() != nil {
				sawRead = true
			}
			exec := m.GetExecServerMessage()
			if exec.GetShellStreamArgs() == nil {
				continue
			}
			note := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
				Action: &cursorpb.ConversationAction_InjectContextAction{InjectContextAction: &cursorpb.InjectContextAction{
					InjectionId:   "inj-skip",
					ExpectedRunId: "sk",
					Payload:       &cursorpb.InjectContextAction_UserContext{UserContext: &cursorpb.UserContextInjection{UserMessage: &cursorpb.UserMessage{Text: "stop after this"}}},
				}},
			}}}
			if _, err := hub.Bidi(bidi(t, "sk", note)); err != nil {
				t.Fatal(err)
			}
			result := execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{
				Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0}},
			}}})
			if _, err := hub.Bidi(bidi(t, "sk", result)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if sawRead || len(fm.requests) < 2 || !strings.Contains(fm.requests[1], "stop after this") || !strings.Contains(fm.requests[1], "Skipped because the user sent a new message.") {
				t.Fatalf("read=%v requests=%v", sawRead, fm.requests)
			}
			return
		case <-ctx.Done():
			t.Fatal("steer skip did not finish")
		}
	}
}

func TestSteerDuringTheAnswerContinuesTheTurn(t *testing.T) {
	fm := &fakeModel{
		pause: make(chan struct{}),
		responses: []string{
			sse(`{"choices":[{"delta":{"content":"first draft"}}]}`),
			sse(`{"choices":[{"delta":{"content":"revised"}}]}`),
		},
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "ans", runRequest("mine", "conv-ans", "draft it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "ans")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	steered := false
	var delivered bool
	for {
		select {
		case <-fm.pause:
			if !steered {
				note := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
					Action: &cursorpb.ConversationAction_InjectContextAction{InjectContextAction: &cursorpb.InjectContextAction{
						InjectionId:   "inj-ans",
						ExpectedRunId: "ans",
						Payload:       &cursorpb.InjectContextAction_UserContext{UserContext: &cursorpb.UserContextInjection{UserMessage: &cursorpb.UserMessage{Text: "change the plan"}}},
					}},
				}}}
				if _, err := hub.Bidi(bidi(t, "ans", note)); err != nil {
					t.Fatal(err)
				}
				steered = true
			}
			fm.pause <- struct{}{}
		case m := <-out:
			if m.GetInteractionUpdate().GetContextInjectionState().GetState().GetDelivered() != nil {
				delivered = true
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if !steered || !delivered || len(fm.requests) < 2 || !strings.Contains(fm.requests[1], "change the plan") || !strings.Contains(fm.requests[1], "first draft") {
				t.Fatalf("steered=%v delivered=%v requests=%v", steered, delivered, fm.requests)
			}
			return
		case <-ctx.Done():
			t.Fatal("steer during answer did not finish")
		}
	}
}

func TestStepLimitStillSendsCheckpoint(t *testing.T) {
	responses := make([]string, maxSteps)
	for i := range responses {
		responses[i] = sse(fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c%d","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`, i))
	}
	fm := &fakeModel{responses: responses}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(t.TempDir()))
	if _, err := hub.Bidi(bidi(t, "cap", runRequest("mine", "conv-cap", "read it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "cap")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 64)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	checkpoint := false
	for {
		select {
		case m := <-out:
			if m.GetConversationCheckpointUpdate() != nil {
				checkpoint = true
			}
			exec := m.GetExecServerMessage()
			if exec.GetReadArgs() == nil {
				continue
			}
			if _, err := hub.Bidi(bidi(t, "cap", execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "x"}}},
			}}}))); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "50") {
				t.Fatalf("err=%v", err)
			}
			for len(out) > 0 {
				if (<-out).GetConversationCheckpointUpdate() != nil {
					checkpoint = true
				}
			}
			if !checkpoint {
				t.Fatal("stopped turn did not send a checkpoint")
			}
			return
		case <-ctx.Done():
			t.Fatal("step limit did not finish")
		}
	}
}

func TestQuietModelOpensThinkingBeforeTheFirstToken(t *testing.T) {
	previous := quietThinkingAfter
	quietThinkingAfter = 40 * time.Millisecond
	t.Cleanup(func() { quietThinkingAfter = previous })

	fm := &fakeModel{
		pause: make(chan struct{}),
		responses: []string{
			sse(`{"choices":[{"delta":{"content":"ok"}}]}`),
		},
	}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "quiet", runRequest("mine", "conv-quiet", "say ok", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "quiet")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	holding := false
	released := false
	sawThinking := false
	var completed bool
	var text string
	for {
		select {
		case <-fm.pause:
			holding = true
			if sawThinking && !released {
				fm.pause <- struct{}{}
				released = true
			}
		case m := <-out:
			if m.GetInteractionUpdate().GetHeartbeat() != nil {
				continue
			}
			if delta := m.GetInteractionUpdate().GetThinkingDelta(); delta != nil {
				if delta.GetText() != " " {
					t.Fatalf("placeholder %q", delta.GetText())
				}
				sawThinking = true
			}
			if m.GetInteractionUpdate().GetThinkingCompleted() != nil {
				completed = true
			}
			if d := m.GetInteractionUpdate().GetTextDelta(); d != nil {
				text += d.GetText()
			}
			if sawThinking && holding && !released {
				fm.pause <- struct{}{}
				released = true
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if !sawThinking || !completed || text != "ok" {
				t.Fatalf("thinking=%v completed=%v text=%q", sawThinking, completed, text)
			}
			if strings.Contains(fm.requests[0], "reasoning_content") {
				t.Fatal("placeholder was sent back to the model")
			}
			return
		case <-ctx.Done():
			t.Fatal("quiet thinking did not finish")
		}
	}
}

func TestInsertArrivesBeforeTheNextModelCall(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"noted"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "ins", runRequest("mine", "conv-ins", "read it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "ins")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	for {
		select {
		case m := <-out:
			exec := m.GetExecServerMessage()
			if exec.GetReadArgs() == nil {
				continue
			}
			note := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
				Action: &cursorpb.ConversationAction_InjectContextAction{InjectContextAction: &cursorpb.InjectContextAction{
					Payload: &cursorpb.InjectContextAction_UserContext{UserContext: &cursorpb.UserContextInjection{UserMessage: &cursorpb.UserMessage{Text: "extra note"}}},
				}},
			}}}
			if _, err := hub.Bidi(bidi(t, "ins", note)); err != nil {
				t.Fatal(err)
			}
			result := execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
				Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Output: &cursorpb.ReadSuccess_Content{Content: "package a"}}},
			}}})
			if _, err := hub.Bidi(bidi(t, "ins", result)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			body := fm.requests[1]
			if len(fm.requests) < 2 || !strings.Contains(body, "extra note") || !strings.Contains(body, "package a") || strings.Contains(body, "Interrupted") {
				t.Fatalf("%v", fm.requests)
			}
			return
		case <-ctx.Done():
			t.Fatal("insert did not finish")
		}
	}
}

func TestSummarizeCompactsAndWritesCheckpoint(t *testing.T) {
	dir := t.TempDir()
	history := NewHistory(dir)
	if err := history.Save("conv-sum", []provider.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
	}); err != nil {
		t.Fatal(err)
	}
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"both questions were answered"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	conversation := "conv-sum"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: "mine"},
		Action:         &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_SummarizeAction{SummarizeAction: &cursorpb.SummarizeAction{}}},
	}}}
	if _, err := hub.Bidi(bidi(t, "sum", msg)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "sum")
	if err != nil {
		t.Fatal(err)
	}
	var sawStart, sawCheckpoint bool
	var hook string
	err = session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
		if m.GetInteractionUpdate().GetSummaryStarted() != nil {
			sawStart = true
		}
		if done := m.GetInteractionUpdate().GetSummaryCompleted(); done != nil {
			hook = done.GetHookMessage()
		}
		if m.GetConversationCheckpointUpdate().GetSelfSummaryCount() == 1 {
			sawCheckpoint = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := history.Load("conv-sum")
	if !sawStart || !sawCheckpoint || hook != "both questions were answered" || len(saved) == 0 || !strings.Contains(saved[0].Content, "both questions were answered") {
		t.Fatalf("start=%v checkpoint=%v hook=%q %+v", sawStart, sawCheckpoint, hook, saved)
	}
}

func TestCheckpointRestoreDropsLaterHistory(t *testing.T) {
	dir := t.TempDir()
	history := NewHistory(dir)
	if err := history.Save("conv-re", []provider.Message{
		{Role: "user", Content: "keep me"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "junk later"},
	}); err != nil {
		t.Fatal(err)
	}
	state := history.LoadState("conv-re")
	state.RootPromptMessagesJson = state.RootPromptMessagesJson[:2]
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	conversation := "conv-re"
	msg := runRequest("mine", conversation, "new question", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().ConversationState = state
	if _, err := hub.Bidi(bidi(t, "re", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "re", model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil })
	if strings.Contains(fm.requests[0], "junk later") || !strings.Contains(fm.requests[0], "keep me") {
		t.Fatalf("%s", fm.requests[0])
	}
}

func TestCheckpointNumbersGrowAcrossTurns(t *testing.T) {
	history := NewHistory(t.TempDir())
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"content":"one"}}]}`),
		sse(`{"choices":[{"delta":{"content":"two"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	var counts []int
	var blobs int
	for i, id := range []string{"t1", "t2"} {
		if _, err := hub.Bidi(bidi(t, id, runRequest("mine", "conv-turns", fmt.Sprintf("question %d", i), &cursorpb.RequestContextEnv{}))); err != nil {
			t.Fatal(err)
		}
		for _, m := range drive(t, hub, id, model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil }) {
			if state := m.GetConversationCheckpointUpdate(); state != nil {
				counts = append(counts, len(state.GetTurns()))
			}
			if m.GetKvServerMessage().GetSetBlobArgs() != nil {
				blobs++
			}
		}
	}
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 || blobs < 2 {
		t.Fatalf("turns=%v blobs=%d", counts, blobs)
	}
}

func TestBreakClosesEveryOpenToolCall(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Shell","arguments":"{\"command\":\"sleep 9\"}"}},{"index":1,"id":"call_2","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"switched"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "br2", runRequest("mine", "conv-br2", "run it", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "br2")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	user := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ConversationAction{ConversationAction: &cursorpb.ConversationAction{
		Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{UserMessage: &cursorpb.UserMessage{Text: "stop that"}}},
	}}}
	for {
		select {
		case m := <-out:
			if m.GetExecServerMessage().GetShellStreamArgs() == nil {
				continue
			}
			if _, err := hub.Bidi(bidi(t, "br2", user)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if len(fm.requests) < 2 || !strings.Contains(fm.requests[1], `"tool_call_id":"call_2"`) {
				t.Fatalf("%v", fm.requests)
			}
			return
		case <-ctx.Done():
			t.Fatal("break did not finish")
		}
	}
}

func TestSubagentHidesShell(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	msg := runRequest("mine", "conv-sub", "look", &cursorpb.RequestContextEnv{})
	name := "explore"
	msg.GetRunRequest().SubagentTypeName = &name
	if _, err := hub.Bidi(bidi(t, "sub", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "sub", model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil })
	if strings.Contains(fm.requests[0], `"Shell"`) || !strings.Contains(fm.requests[0], `"Read"`) {
		t.Fatal(fm.requests[0])
	}
}

func TestSwitchModeChangesTheNextToolList(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"sw","function":{"name":"SwitchMode","arguments":"{\"target_mode_id\":\"plan\",\"explanation\":\"need a plan\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"planning"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "mode", runRequest("mine", "conv-mode", "change", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "mode")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	for {
		select {
		case m := <-out:
			query := m.GetInteractionQuery().GetSwitchModeRequestQuery()
			if query == nil {
				continue
			}
			reply := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_InteractionResponse{InteractionResponse: &cursorpb.InteractionResponse{
				Id: m.GetInteractionQuery().GetId(),
				Result: &cursorpb.InteractionResponse_SwitchModeRequestResponse{SwitchModeRequestResponse: &cursorpb.SwitchModeRequestResponse{
					Result: &cursorpb.SwitchModeRequestResponse_Approved_{Approved: &cursorpb.SwitchModeRequestResponse_Approved{}},
				}},
			}}}
			if _, err := hub.Bidi(bidi(t, "mode", reply)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if len(fm.requests) < 2 || !strings.Contains(fm.requests[1], "CreatePlan") || strings.Contains(fm.requests[1], `"name":"Delete"`) {
				t.Fatalf("%s", fm.requests[1])
			}
			return
		case <-ctx.Done():
			t.Fatal("mode switch did not finish")
		}
	}
}

func TestFastSupportReachesTheModel(t *testing.T) {
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m", FastSupport: true}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	msg := runRequest("mine", "conv-fast", "hi", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().RequestedModel.ModelId = "mine[context=200k,fast=true]"
	if _, err := hub.Bidi(bidi(t, "fast", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "fast", model, nil)
	if !strings.Contains(fm.requests[0], `"service_tier":"fast"`) || strings.Contains(fm.requests[0], "reasoning_effort") {
		t.Fatalf("%s", fm.requests[0])
	}
}

func TestEmptyModelForwardsWithoutWaiting(t *testing.T) {
	hub := New(func(string) (config.Model, bool) { return config.Model{ID: "mine"}, true }, nil)
	msg := runRequest("mine", "conv-empty", "hi", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().RequestedModel.ModelId = ""
	route, err := hub.Bidi(bidi(t, "empty", msg))
	if err != nil || route.Local {
		t.Fatalf("%+v %v", route, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "empty")
	if err != nil || session != nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
}

func TestTaskReturnsTheSubagentMessage(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"task1","function":{"name":"Task","arguments":"{\"prompt\":\"find the loop\",\"description\":\"find\",\"subagent_type\":\"explore\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"done"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "task", runRequest("mine", "conv-task", "delegate", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "task")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	final := "found the loop"
	for {
		select {
		case m := <-out:
			exec := m.GetExecServerMessage()
			if exec.GetSubagentArgs() == nil {
				continue
			}
			if exec.GetSubagentArgs().GetModelId() != "mine[context=200k,reasoning=high,fast=false]" {
				t.Fatalf("model %q", exec.GetSubagentArgs().GetModelId())
			}
			result := execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_SubagentResult{SubagentResult: &cursorpb.SubagentResult{
				Result: &cursorpb.SubagentResult_Success{Success: &cursorpb.SubagentSuccess{FinalMessage: &final}},
			}}})
			if _, err := hub.Bidi(bidi(t, "task", result)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if len(fm.requests) < 2 || !strings.Contains(fm.requests[1], "found the loop") {
				t.Fatalf("%v", fm.requests)
			}
			return
		case <-ctx.Done():
			t.Fatal("task did not finish")
		}
	}
}

func TestLongHistoryIsSummarizedBeforeTheReply(t *testing.T) {
	dir := t.TempDir()
	history := NewHistory(dir)
	if err := history.Save("conv-long", []provider.Message{
		{Role: "user", Content: strings.Repeat("alpha ", 400)},
		{Role: "assistant", Content: strings.Repeat("beta ", 400)},
	}); err != nil {
		t.Fatal(err)
	}
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"content":"durable summary"}}]}`),
		sse(`{"choices":[{"delta":{"content":"answer"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m", ContextWindow: 200}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	conversation := "conv-long"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: "mine"},
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
			UserMessage:    &cursorpb.UserMessage{Text: "continue"},
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	if _, err := hub.Bidi(bidi(t, "long", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "long", model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil })
	if len(fm.requests) < 2 || !strings.Contains(fm.requests[0], "compacting conversation history") || !strings.Contains(fm.requests[1], "continue") {
		t.Fatalf("requests=%d first=%s", len(fm.requests), fm.requests[0])
	}
}

func TestConfiguredWindowCapsTheClientSelection(t *testing.T) {
	dir := t.TempDir()
	history := NewHistory(dir)
	if err := history.Save("conv-cap", []provider.Message{
		{Role: "user", Content: strings.Repeat("alpha ", 400)},
		{Role: "assistant", Content: strings.Repeat("beta ", 400)},
	}); err != nil {
		t.Fatal(err)
	}
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"content":"durable summary"}}]}`),
		sse(`{"choices":[{"delta":{"content":"answer"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m", ContextWindow: 200}
	hub := New(func(string) (config.Model, bool) { return model, true }, history)
	conversation := "conv-cap"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conversation,
		RequestedModel: &cursorpb.RequestedModel{ModelId: "mine[context=200k]"},
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
			UserMessage:    &cursorpb.UserMessage{Text: "continue"},
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	if _, err := hub.Bidi(bidi(t, "cap", msg)); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "cap", model, func(*cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage { return nil })
	if len(fm.requests) < 2 || !strings.Contains(fm.requests[0], "compacting conversation history") {
		t.Fatalf("requests=%d first=%s", len(fm.requests), firstRequest(fm.requests))
	}
}

func firstRequest(requests []string) string {
	if len(requests) == 0 {
		return ""
	}
	return requests[0]
}

func TestTodoWriteAndCreatePlan(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"todo","function":{"name":"TodoWrite","arguments":"{\"todos\":[{\"id\":\"1\",\"content\":\"ship\",\"status\":\"pending\"}]}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"plan","function":{"name":"CreatePlan","arguments":"{\"name\":\"ship\",\"overview\":\"do it\",\"plan\":\"step\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"ok"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	msg := runRequest("mine", "conv-plan", "plan it", &cursorpb.RequestContextEnv{})
	msg.GetRunRequest().GetAction().GetUserMessageAction().GetUserMessage().Mode = cursorpb.AgentMode_AGENT_MODE_PLAN
	if _, err := hub.Bidi(bidi(t, "plan", msg)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "plan")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 32)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	var sawTodo bool
	for {
		select {
		case m := <-out:
			if m.GetInteractionUpdate().GetToolCallCompleted().GetToolCall().GetUpdateTodosToolCall() != nil {
				sawTodo = true
			}
			query := m.GetInteractionQuery().GetCreatePlanRequestQuery()
			if query == nil {
				continue
			}
			reply := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_InteractionResponse{InteractionResponse: &cursorpb.InteractionResponse{
				Id: m.GetInteractionQuery().GetId(),
				Result: &cursorpb.InteractionResponse_CreatePlanRequestResponse{CreatePlanRequestResponse: &cursorpb.CreatePlanRequestResponse{
					Result: &cursorpb.CreatePlanResult{Result: &cursorpb.CreatePlanResult_Success{Success: &cursorpb.CreatePlanSuccess{}}},
				}},
			}}}
			if _, err := hub.Bidi(bidi(t, "plan", reply)); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if !sawTodo || len(fm.requests) < 3 || !strings.Contains(fm.requests[2], "accepted the plan") {
				t.Fatalf("todo=%v requests=%d", sawTodo, len(fm.requests))
			}
			return
		case <-ctx.Done():
			t.Fatal("plan tools did not finish")
		}
	}
}

func TestSemSearchUsesClientGrepAndOnlyKeepsNumberedHits(t *testing.T) {
	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"ss","function":{"name":"SemSearch","arguments":"{\"query\":\"alpha marker\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"1"}}]}`),
		sse(`{"choices":[{"delta":{"content":"found"}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, nil)
	if _, err := hub.Bidi(bidi(t, "ss", runRequest("mine", "conv-ss", "search", &cursorpb.RequestContextEnv{WorkspacePaths: []string{"/w"}}))); err != nil {
		t.Fatal(err)
	}
	drive(t, hub, "ss", model, func(exec *cursorpb.ExecServerMessage) *cursorpb.ExecClientMessage {
		grep := exec.GetGrepArgs()
		if grep.GetOutputMode() == "files_with_matches" {
			return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_GrepResult{GrepResult: &cursorpb.GrepResult{
				Result: &cursorpb.GrepResult_Success{Success: &cursorpb.GrepSuccess{WorkspaceResults: map[string]*cursorpb.GrepUnionResult{
					"w": {Result: &cursorpb.GrepUnionResult_Files{Files: &cursorpb.GrepFilesResult{Files: []string{"/w/note.txt"}}}},
				}}},
			}}}
		}
		return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_GrepResult{GrepResult: &cursorpb.GrepResult{
			Result: &cursorpb.GrepResult_Success{Success: &cursorpb.GrepSuccess{WorkspaceResults: map[string]*cursorpb.GrepUnionResult{
				"w": {Result: &cursorpb.GrepUnionResult_Content{Content: &cursorpb.GrepContentResult{Matches: []*cursorpb.GrepFileMatch{{
					File: "/w/note.txt", Matches: []*cursorpb.GrepContentMatch{{LineNumber: 1, Content: "alpha beta semantic marker"}},
				}}}}},
			}}},
		}}}
	})
	if len(fm.requests) < 3 || !strings.Contains(fm.requests[1], "/w/note.txt") || !strings.Contains(fm.requests[2], "/w/note.txt") || strings.Contains(fm.requests[2], "made-up") {
		t.Fatalf("requests=%d", len(fm.requests))
	}
}

func TestToolSilenceBecomesAFailureTheModelCanContinue(t *testing.T) {
	prev := toolWaitLimit
	toolWaitLimit = 200 * time.Millisecond
	t.Cleanup(func() { toolWaitLimit = prev })

	fm := &fakeModel{responses: []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"/w/a.go\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"The read did not finish, so I will stop there."}}]}`),
	}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", DisplayName: "Mine", Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	hub := New(func(id string) (config.Model, bool) { return model, id == "mine" }, NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{WorkspacePaths: []string{"/w"}, OsVersion: "linux"}
	if _, err := hub.Bidi(bidi(t, "hang", runRequest("mine", "conv-hang", "read a.go", env))); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "hang")
	if err != nil || session == nil {
		t.Fatalf("session=%v err=%v", session, err)
	}
	out := make(chan *cursorpb.AgentServerMessage, 64)
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, dialer.Direct(), func(m *cursorpb.AgentServerMessage) error {
			out <- m
			return nil
		})
	}()
	for {
		select {
		case m := <-out:
			exec := m.GetExecServerMessage()
			if exec.GetRequestContextArgs() != nil {
				if _, err := hub.Bidi(bidi(t, "hang", execResult(exec.GetId(), &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{
					Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: env}}},
				}}}))); err != nil {
					t.Fatal(err)
				}
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			fm.mu.Lock()
			defer fm.mu.Unlock()
			if len(fm.requests) < 2 || !strings.Contains(fm.requests[1], "did not finish within the time limit") {
				t.Fatalf("model did not receive the timeout: %d", len(fm.requests))
			}
			return
		case <-ctx.Done():
			t.Fatal("session did not finish")
		}
	}
}

func TestOfficialModelIsForwarded(t *testing.T) {
	hub := New(func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	route, err := hub.Bidi(bidi(t, "o", runRequest("claude-opus", "c", "hi", nil)))
	if err != nil || route.Local {
		t.Fatalf("%+v %v", route, err)
	}
	session, err := hub.Wait(context.Background(), "o")
	if err != nil || session != nil {
		t.Fatalf("official run got a local session: %v %v", session, err)
	}
	route, _ = hub.Bidi(bidi(t, "o", execResult(1, &cursorpb.ExecClientMessage{})))
	if route.Local {
		t.Fatal("exec result for an official run handled locally")
	}
}
