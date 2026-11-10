package mitm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/protox"
	"google.golang.org/protobuf/proto"
)

func TestBidiOriginalHeaderDoesNotStealLiveShellResults(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	responses := []string{
		`{"choices":[{"delta":{"content":"old answer"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"exec-one","function":{"name":"Shell","arguments":"{\"command\":\"printf confirmed-result\"}"}}]}}]}`,
		`{"choices":[{"delta":{"content":"new answer"}}]}`,
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		index := len(requests)
		requests = append(requests, string(body))
		mu.Unlock()
		if index >= len(responses) {
			t.Error("unexpected extra model invocation")
			http.Error(w, "unexpected", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+responses[index]+"\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: provider.URL, APIKey: "k", Model: "m"}
	lookup := func(id string) (config.Model, bool) { return model, id == model.ID }
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, lookup, agent.NewHistory(t.TempDir()))
	env := &cursorpb.RequestContextEnv{OsVersion: "linux", WorkspacePaths: []string{"/w"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	appendMessage := func(id, original string, msg *cursorpb.AgentClientMessage) {
		t.Helper()
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		body := protox.AppendString(nil, 1, protox.EncodeHex(raw))
		body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, id))
		request := httptest.NewRequest(http.MethodPost, "/aiserver.v1.BidiService/BidiAppend", bytes.NewReader(protox.Frame(0, body)))
		request.Header.Set("X-Original-Request-Id", original)
		recorder := httptest.NewRecorder()
		s.bidi(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("append status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	run := func(id string) *cursorpb.AgentClientMessage {
		conv := "conversation"
		return &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{ConversationId: &conv, RequestedModel: &cursorpb.RequestedModel{ModelId: "mine"}, Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{UserMessage: &cursorpb.UserMessage{Text: id}, RequestContext: &cursorpb.RequestContext{Env: env}}}}}}}
	}
	for _, id := range []string{"old", "new"} {
		appendMessage(id, "", run(id))
		session, err := s.hub.Wait(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		original := ""
		if id == "new" {
			original = "old"
		}
		err = session.Run(ctx, dialer.Direct(), func(message *cursorpb.AgentServerMessage) error {
			exec := message.GetExecServerMessage()
			if exec == nil {
				return nil
			}
			send := func(result *cursorpb.ExecClientMessage) {
				result.Id = exec.GetId()
				result.ExecId = exec.GetExecId()
				appendMessage(id, original, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ExecClientMessage{ExecClientMessage: result}})
			}
			switch {
			case exec.GetRequestContextArgs() != nil:
				send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_RequestContextResult{RequestContextResult: &cursorpb.RequestContextResult{Result: &cursorpb.RequestContextResult_Success{Success: &cursorpb.RequestContextSuccess{RequestContext: &cursorpb.RequestContext{Env: env}}}}}})
			case exec.GetShellStreamArgs() != nil:
				appendMessage(id, original, &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &cursorpb.ClientHeartbeat{}}})
				for _, stream := range []*cursorpb.ShellStream{
					{Event: &cursorpb.ShellStream_Start{Start: &cursorpb.ShellStreamStart{}}},
					{Event: &cursorpb.ShellStream_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: "confirmed-result"}}},
					{Event: &cursorpb.ShellStream_Exit{Exit: &cursorpb.ShellStreamExit{Code: 0}}},
				} {
					send(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: stream}})
				}
				got, err := s.hub.Wait(ctx, id)
				if err != nil || got != session {
					t.Error("live request binding changed", err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		s.hub.Done(id)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 || !strings.Contains(requests[2], "confirmed-result") {
		t.Fatal("HTTP 200 did not deliver the shell result to its model")
	}
}
