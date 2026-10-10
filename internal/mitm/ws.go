package mitm

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

func websocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// bridgeWebSocket 把升级请求原样接到上游。普通转发会去掉 Upgrade，服务端就把它当成一条不存在的 GET。
func (s *Server) bridgeWebSocket(w http.ResponseWriter, r *http.Request) {
	s.countOfficial()
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.forward(w, r)
		return
	}
	host := requestHost(r)
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	addr := host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "443")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	raw, err := s.dialContext(ctx, "tcp", addr)
	cancel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		s.noteWebSocket(r, http.StatusBadGateway, nil)
		return
	}
	cfg := s.wsTLS
	if cfg == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name}
	}
	up := tls.Client(raw, cfg)
	if err := up.Handshake(); err != nil {
		_ = raw.Close()
		http.Error(w, err.Error(), http.StatusBadGateway)
		s.noteWebSocket(r, http.StatusBadGateway, nil)
		return
	}
	if err := writeUpgrade(up, r); err != nil {
		_ = up.Close()
		http.Error(w, err.Error(), http.StatusBadGateway)
		s.noteWebSocket(r, http.StatusBadGateway, nil)
		return
	}
	br := bufio.NewReader(up)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		_ = up.Close()
		http.Error(w, err.Error(), http.StatusBadGateway)
		s.noteWebSocket(r, http.StatusBadGateway, nil)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(string(body)))
		resp.ContentLength = int64(len(body))
		_ = resp.Write(client)
		_ = client.Close()
		_ = up.Close()
		s.noteWebSocket(r, resp.StatusCode, body)
		return
	}
	resp.Body = nil
	resp.ContentLength = -1
	if err := resp.Write(client); err != nil {
		_ = client.Close()
		_ = up.Close()
		s.noteWebSocket(r, resp.StatusCode, nil)
		return
	}
	extra, _ := br.Peek(br.Buffered())
	if len(extra) > 0 {
		_, _ = client.Write(extra)
		_, _ = br.Discard(len(extra))
	}
	s.noteWebSocket(r, resp.StatusCode, nil)
	var toNet, fromNet io.WriteCloser
	if open := currentRawOpen(); open != nil {
		toNet, fromNet = open(addr, "websocket")
	}
	s.steerWebSocket(client, up, toNet, fromNet)
}

// steerWebSocket 先看这条 Agents 连接要不要本地模型。官方模型的字节原样交给上游。
func (s *Server) steerWebSocket(client, up net.Conn, toNet, fromNet io.WriteCloser) {
	defer client.Close()
	defer up.Close()
	defer closeRaw(toNet)
	defer closeRaw(fromNet)
	buffered, local, localID := s.readAgentDecision(client)
	_ = client.SetReadDeadline(time.Time{})
	if local != nil {
		_ = up.Close()
		noteRaw(toNet, buffered)
		s.serveLocalAgent(client, local, localID, toNet, fromNet)
		return
	}
	noteRaw(toNet, buffered)
	if len(buffered) > 0 {
		if _, err := up.Write(buffered); err != nil {
			return
		}
	}
	splice(client, up, toNet, fromNet)
}

func (s *Server) readAgentDecision(client net.Conn) ([]byte, *agent.Session, string) {
	var buffered []byte
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		op, payload, raw, err := readWSFrame(client)
		if err != nil {
			return buffered, nil, ""
		}
		buffered = append(buffered, raw...)
		if len(buffered) > 8<<20 || (op != 0x1 && op != 0x2 && op != 0x9) {
			return buffered, nil, ""
		}
		if op == 0x9 {
			_ = writeWSFrame(client, 0xA, payload)
			_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
			continue
		}
		frame := protox.ClassifyAgentPayload(payload)
		if frame.Kind == "hello" || frame.Kind == "meta" {
			_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
			continue
		}
		if frame.Kind != "agent" || len(frame.Message) == 0 || s.hub == nil {
			return buffered, nil, ""
		}
		var msg cursorpb.AgentClientMessage
		if proto.Unmarshal(frame.Message, &msg) != nil || msg.GetRunRequest() == nil {
			return buffered, nil, ""
		}
		_, session, start := s.hub.Client(frame.RequestID, &msg)
		if start && session != nil {
			return buffered, session, frame.RequestID
		}
		return buffered, nil, ""
	}
}

func (s *Server) serveLocalAgent(client net.Conn, session *agent.Session, id string, toNet, fromNet io.Writer) {
	s.countLocal()
	defer s.hub.Done(id)
	var mu sync.Mutex
	var seq uint64
	emit := func(m *cursorpb.AgentServerMessage) error {
		raw, err := proto.Marshal(m)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		seq++
		return writeLogged(client, fromNet, protox.EncodeAgentServer(id, seq, raw))
	}
	mu.Lock()
	_ = writeLogged(client, fromNet, []byte{0x0a, 0x02, 0x08, 0x01})
	_ = writeLogged(client, fromNet, protox.EncodeAgentMetaAck(id))
	mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d, err := dialer.ForModel(s.proxy(), session.Model.UseProxy)
		session.Proxy = s.proxy()
		session.Web = s.dialContext
		if err == nil {
			err = session.Run(ctx, d, emit)
		}
		if err != nil && ctx.Err() == nil {
			_ = emit(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: &cursorpb.InteractionUpdate{
				Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: provider.Explain(err)}},
			}}})
		}
	}()
	for {
		op, payload, raw, err := readWSFrame(client)
		if err != nil {
			cancel()
			break
		}
		noteRaw(toNet, raw)
		if op == 0x9 {
			mu.Lock()
			_ = writeWSFrame(client, 0xA, payload)
			mu.Unlock()
			continue
		}
		if op == 0x8 {
			cancel()
			break
		}
		frame := protox.ClassifyAgentPayload(payload)
		if frame.Kind != "agent" || len(frame.Message) == 0 {
			continue
		}
		var msg cursorpb.AgentClientMessage
		if proto.Unmarshal(frame.Message, &msg) != nil {
			continue
		}
		use := id
		if frame.RequestID != "" {
			use = frame.RequestID
		}
		_, _, _ = s.hub.Client(use, &msg)
	}
	<-done
}

func writeLogged(dst io.Writer, log io.Writer, payload []byte) error {
	frame := wsFrameBytes(0x2, payload)
	noteRaw(log, frame)
	_, err := dst.Write(frame)
	return err
}

func noteRaw(w io.Writer, p []byte) {
	if w == nil || len(p) == 0 {
		return
	}
	_, _ = w.Write(p)
}

func closeRaw(w io.WriteCloser) {
	if w != nil {
		_ = w.Close()
	}
}

func (s *Server) noteWebSocket(r *http.Request, status int, respBody []byte) {
	reportTrace(currentTrace(), TraceEvent{
		Host: r.Host, Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
		Status: status, ReqHeader: r.Header, RespBody: respBody,
	})
}

func writeUpgrade(dst io.Writer, r *http.Request) error {
	uri := r.URL.RequestURI()
	if uri == "" {
		uri = "/"
	}
	if _, err := fmt.Fprintf(dst, "%s %s HTTP/1.1\r\n", r.Method, uri); err != nil {
		return err
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if _, err := fmt.Fprintf(dst, "Host: %s\r\n", host); err != nil {
		return err
	}
	for key, values := range r.Header {
		for _, value := range values {
			if _, err := fmt.Fprintf(dst, "%s: %s\r\n", key, value); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(dst, "\r\n")
	return err
}
