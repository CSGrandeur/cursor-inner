package mitm

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
)

// 官方拒绝升级（401）时本地给 101 并接管：没有上游连接，本地模型的这一轮也必须能答，不能因上游为空而崩。
func TestWebSocketUnauthorizedUpgradeServesLocalModel(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n")
	}()
	conv := "conv"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conv,
		RequestedModel: &cursorpb.RequestedModel{ModelId: "mine"},
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
			UserMessage:    &cursorpb.UserMessage{Text: "ping"},
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	item := protox.AppendBytes(protox.AppendString(nil, 1, "req-401"), 3, raw)
	left, right := net.Pipe()
	defer left.Close()
	req := httptest.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+"/agent/v1/run", nil)
	req.Host = ln.Addr().String()
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return []catalog.Entry{{}} }, func(id string) (config.Model, bool) {
		if id != "mine" {
			return config.Model{}, false
		}
		return config.Model{ID: "mine", Type: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}, true
	}, nil)
	s.wsTLS = &tls.Config{InsecureSkipVerify: true}
	panicked := make(chan any, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { panicked <- recover() }()
		s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, req)
	}()
	br := bufio.NewReader(left)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status %q %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	if _, err := left.Write(maskWSFrame(protox.AppendBytes(nil, 3, item))); err != nil {
		t.Fatal(err)
	}
	_ = left.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, payload, _, ferr := readWSFrame(br)
	_ = left.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not finish")
	}
	if p := <-panicked; p != nil {
		t.Fatalf("bridge panicked: %v", p)
	}
	if ferr != nil || len(payload) == 0 {
		t.Fatalf("no local frame: %v", ferr)
	}
}
