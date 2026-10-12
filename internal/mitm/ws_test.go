package mitm

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
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

func TestWebSocketUpgradeIsForwardedIntact(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotUpgrade := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		gotUpgrade <- req.Header.Get("Upgrade")
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, payload, _, err := readWSFrame(br)
		if err != nil || string(payload) != "hi" {
			return
		}
		_ = writeWSFrame(conn, 0x2, []byte("pong"))
	}()

	left, right := net.Pipe()
	defer left.Close()
	req := httptest.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+"/agent/v1/run", nil)
	req.Host = ln.Addr().String()
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, nil, nil)
	s.wsTLS = &tls.Config{InsecureSkipVerify: true}
	done := make(chan struct{})
	go func() {
		s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, req)
		close(done)
	}()
	br := bufio.NewReader(left)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	select {
	case upgrade := <-gotUpgrade:
		if !strings.EqualFold(upgrade, "websocket") {
			t.Fatalf("upstream upgrade %q", upgrade)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not see the request")
	}
	if _, err := left.Write(maskWSFrame([]byte("hi"))); err != nil {
		t.Fatal(err)
	}
	_, payload, _, err := readWSFrame(br)
	if err != nil || string(payload) != "pong" {
		t.Fatalf("back %q %v", payload, err)
	}
	_ = left.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not finish")
	}
}

type hijackRW struct {
	net.Conn
	header http.Header
	code   int
}

func (h *hijackRW) Header() http.Header { return h.header }
func (h *hijackRW) Write(p []byte) (int, error) {
	if h.code == 0 {
		h.WriteHeader(http.StatusOK)
	}
	return h.Conn.Write(p)
}
func (h *hijackRW) WriteHeader(code int) {
	if h.code == 0 {
		h.code = code
	}
}
func (h *hijackRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.Conn, bufio.NewReadWriter(bufio.NewReader(h.Conn), bufio.NewWriter(h.Conn)), nil
}

func TestWebSocketLocalModelIsNotForwarded(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	seen := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 256)
		n, _ := br.Read(buf)
		seen <- append([]byte(nil), buf[:n]...)
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
	item := protox.AppendBytes(protox.AppendString(nil, 1, "req-1"), 3, raw)
	left, right := net.Pipe()
	defer left.Close()
	req := httptest.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+"/agent/v1/run", nil)
	req.Host = ln.Addr().String()
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, func(id string) (config.Model, bool) {
		if id != "mine" {
			return config.Model{}, false
		}
		return config.Model{ID: "mine", Type: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}, true
	}, nil)
	s.wsTLS = &tls.Config{InsecureSkipVerify: true}
	done := make(chan struct{})
	go func() {
		s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, req)
		close(done)
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
	if _, payload, _, err := readWSFrame(br); err != nil || len(payload) == 0 {
		t.Fatalf("local frame %q %v", payload, err)
	}
	_ = left.Close()
	select {
	case got := <-seen:
		if strings.Contains(string(got), "mine") {
			t.Fatalf("upstream saw the local run (%d bytes)", len(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not finish")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not finish")
	}
}

func TestWebSocketHandshakeKeepsLocalModelOffUpstream(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	seen := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if _, payload, _, err := readWSFrame(br); err != nil || string(payload) != string([]byte{0x0a, 0x02, 0x08, 0x01}) {
			seen <- []byte("bad-hello")
			return
		}
		if err := writeWSFrame(conn, 0x2, []byte{0x0a, 0x02, 0x08, 0x01}); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		buf := make([]byte, 512)
		n, _ := br.Read(buf)
		seen <- append([]byte(nil), buf[:n]...)
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
	ident := protox.AppendString(nil, 1, "run-1")
	ident = protox.AppendString(ident, 2, "req-1")
	runStart := protox.AppendBytes(nil, 2, protox.AppendBytes(nil, 1, ident))
	item := protox.AppendString(nil, 1, "run-1")
	item = protox.AppendVarint(item, 2, 0)
	item = protox.AppendBytes(item, 3, raw)
	clientMsg := protox.AppendBytes(nil, 3, item)
	left, right := net.Pipe()
	defer left.Close()
	req := httptest.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+"/agent/v1/run", nil)
	req.Host = ln.Addr().String()
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, func(id string) (config.Model, bool) {
		if id != "mine" {
			return config.Model{}, false
		}
		return config.Model{ID: "mine", Type: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}, true
	}, nil)
	s.wsTLS = &tls.Config{InsecureSkipVerify: true}
	done := make(chan struct{})
	go func() {
		s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, req)
		close(done)
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
	if _, err := left.Write(maskWSFrame([]byte{0x0a, 0x02, 0x08, 0x01})); err != nil {
		t.Fatal(err)
	}
	if _, payload, _, err := readWSFrame(br); err != nil || string(payload) != string([]byte{0x0a, 0x02, 0x08, 0x01}) {
		t.Fatalf("server hello %q %v", payload, err)
	}
	// 池化连接会在 hello 之后停几秒，等用户真正发送。短于这个间隔就把连接交给官方，后到的自定义模型会被官方拒绝。
	// hello 和这一轮之间多出来的帧也一样：认不出就不能把整条连接交出去。
	if _, err := left.Write(maskWSFrame(protox.AppendString(nil, 9, "later"))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	if _, err := left.Write(maskWSFrame(runStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := left.Write(maskWSFrame(clientMsg)); err != nil {
		t.Fatal(err)
	}
	_, payload, _, err := readWSFrame(br)
	if err != nil || !strings.Contains(string(payload), "run-1") || !strings.Contains(string(payload), "req-1") {
		t.Fatalf("run accepted %q %v", payload, err)
	}
	_ = left.SetReadDeadline(time.Now().Add(3 * time.Second))
	var seq uint64
	var saw bool
	for !saw {
		_, payload, _, err = readWSFrame(br)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := protox.Fields(payload)
		if err != nil {
			continue
		}
		for _, f := range fields {
			if f.Num != 3 || f.Wire != 2 {
				continue
			}
			inner, err := protox.Fields(f.Raw)
			if err != nil {
				continue
			}
			for _, g := range inner {
				if g.Num == 2 && g.Wire == 0 {
					seq = g.Val
					saw = true
				}
			}
		}
	}
	if seq != 0 {
		t.Fatalf("first server sequence %d", seq)
	}
	_ = left.Close()
	select {
	case got := <-seen:
		if strings.Contains(string(got), "mine") || string(got) == "bad-hello" {
			t.Fatalf("upstream saw the local run (%d bytes)", len(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not finish")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not finish")
	}
}

func TestWebSocketOfficialRunForwardsThenLocalStays(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	seen := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if _, payload, _, err := readWSFrame(br); err != nil || string(payload) != string([]byte{0x0a, 0x02, 0x08, 0x01}) {
			seen <- []byte("bad-hello")
			return
		}
		if err := writeWSFrame(conn, 0x2, []byte{0x0a, 0x02, 0x08, 0x01}); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		var acc []byte
		for {
			_, payload, _, err := readWSFrame(br)
			if err != nil {
				break
			}
			acc = append(acc, payload...)
		}
		seen <- acc
	}()
	officialMeta, officialAgent := agentRunFrames(t, "run-1", "req-1", "grok-test", "ping-official")
	localMeta, localAgent := agentRunFrames(t, "run-2", "req-2", "mine", "ping-local")
	left, right := net.Pipe()
	defer left.Close()
	req := httptest.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+"/agent/v1/run", nil)
	req.Host = ln.Addr().String()
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, func(id string) (config.Model, bool) {
		if id != "mine" {
			return config.Model{}, false
		}
		return config.Model{ID: "mine", Type: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}, true
	}, nil)
	s.wsTLS = &tls.Config{InsecureSkipVerify: true}
	done := make(chan struct{})
	go func() {
		s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, req)
		close(done)
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
	if _, err := left.Write(maskWSFrame([]byte{0x0a, 0x02, 0x08, 0x01})); err != nil {
		t.Fatal(err)
	}
	if _, payload, _, err := readWSFrame(br); err != nil || string(payload) != string([]byte{0x0a, 0x02, 0x08, 0x01}) {
		t.Fatalf("server hello %q %v", payload, err)
	}
	for _, frame := range [][]byte{officialMeta, officialAgent, localMeta, localAgent} {
		if _, err := left.Write(maskWSFrame(frame)); err != nil {
			t.Fatal(err)
		}
	}
	_ = left.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, payload, _, err := readWSFrame(br)
	if err != nil || !strings.Contains(string(payload), "run-2") || !strings.Contains(string(payload), "req-2") {
		t.Fatalf("second run accepted %q %v", payload, err)
	}
	_ = left.Close()
	select {
	case got := <-seen:
		if !strings.Contains(string(got), "grok-test") {
			t.Fatalf("upstream missed the official run (%d bytes)", len(got))
		}
		if strings.Contains(string(got), "mine") || strings.Contains(string(got), "ping-local") {
			t.Fatalf("upstream saw the later local run (%d bytes)", len(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not finish")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not finish")
	}
}

func agentRunFrames(t *testing.T, runID, reqID, model, text string) (meta, agent []byte) {
	t.Helper()
	conv := "conv"
	msg := &cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{
		ConversationId: &conv,
		RequestedModel: &cursorpb.RequestedModel{ModelId: model},
		Action: &cursorpb.ConversationAction{Action: &cursorpb.ConversationAction_UserMessageAction{UserMessageAction: &cursorpb.UserMessageAction{
			UserMessage:    &cursorpb.UserMessage{Text: text},
			RequestContext: &cursorpb.RequestContext{Env: &cursorpb.RequestContextEnv{}},
		}}},
	}}}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	ident := protox.AppendString(nil, 1, runID)
	ident = protox.AppendString(ident, 2, reqID)
	meta = protox.AppendBytes(nil, 2, protox.AppendBytes(nil, 1, ident))
	item := protox.AppendString(nil, 1, runID)
	item = protox.AppendVarint(item, 2, 0)
	item = protox.AppendBytes(item, 3, raw)
	agent = protox.AppendBytes(nil, 3, item)
	return meta, agent
}

func testServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "ws"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"api2.cursor.sh"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}
