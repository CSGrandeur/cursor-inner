package mitm

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
)

// 现场（2026-10-23）：用户选 Claude Opus 官方模型，BidiAppend 解析为 where=官方，
// 但正文仍含自定义模型 id（同会话历史 / 目录）。旧逻辑 leakedBidiModel → 502，
// 客户端报 Server error / The server failed 4 times。
func TestOfficialBidiWithCustomIDInBodyForwards(t *testing.T) {
	const custom = "113b6dd3062290bb"
	const official = "claude-opus-5-5"
	upstream := 0
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry {
			return []catalog.Entry{{ID: custom, DisplayName: "DeepSeek-V4-Flash-Vision-Exp"}}
		},
		func(id string) (config.Model, bool) {
			if id == custom {
				return config.Model{ID: custom}, true
			}
			return config.Model{}, false
		}, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		upstream++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("ok"))),
		}, nil
	})}

	body := officialBidiMentioningCustom(t, "7a82072f-fd4c-46bc-bcba-902a63413f59", official, custom)
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", bytes.NewReader(body)))
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("must not 502 official turn that merely mentions custom id; body=%q", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if upstream != 1 {
		t.Fatalf("want 1 upstream forward, got %d", upstream)
	}
}

func TestCustomBidiStillRejectedWhenNotLocal(t *testing.T) {
	// 解析失败 + 正文含自定义 id：仍应拒绝交给官方（保持防泄漏）。
	const custom = "113b6dd3062290bb"
	upstream := 0
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: custom}} },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		upstream++
		return nil, http.ErrHandlerTimeout
	})}
	// localLoopAppend：读不成 AgentClientMessage，但带自定义 id → 本机收下（200），不上游。
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", bytes.NewReader(localLoopAppend("rid-x", custom))))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if upstream != 0 {
		t.Fatalf("custom unparseable must not go upstream, got %d", upstream)
	}
}

func officialBidiMentioningCustom(t *testing.T, requestID, officialModel, customID string) []byte {
	t.Helper()
	conv := "conv-official"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conv,
		RequestedModel: &cursorpb.RequestedModel{ModelId: officialModel + "[context=200k]"},
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
			UserMessage: &cursorpb.UserMessage{Text: "hello prior custom " + customID},
		}}},
	}}}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	// UserMessage 文本已含 customID；EncodeHex 后 leakedBidiModel 能靠 hex(id) 扫到，
	// 且 protobuf 仍可被 hub 正常解析成官方 model。
	body := protox.AppendString(nil, 1, protox.EncodeHex(raw))
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, requestID))
	return protox.Frame(0, body)
}
