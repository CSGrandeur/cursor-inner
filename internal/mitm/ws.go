package mitm

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
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
	buffered, local, localID, runID, requestID, helloRelayed, start := s.readAgentDecision(client, up)
	_ = client.SetReadDeadline(time.Time{})
	_ = up.SetReadDeadline(time.Time{})
	if local != nil {
		_ = up.Close()
		noteRaw(toNet, buffered)
		if start {
			s.serveLocalAgent(client, local, localID, runID, requestID, helloRelayed, toNet, fromNet)
		} else if local.Finished() {
			s.serveLocalIdle(client, runID, requestID, helloRelayed, fromNet)
		} else {
			s.serveLocalAttach(client, localID, runID, requestID, helloRelayed, toNet, fromNet)
		}
		return
	}
	noteRaw(toNet, buffered)
	if id := leakedModel(buffered, s.entries()); id != "" {
		slog.Error("自定义模型的对话没有留在本机，已断开，不交给官方", "model", id)
		return
	}
	if len(buffered) > 0 {
		if _, err := up.Write(buffered); err != nil {
			return
		}
	}
	splice(client, up, toNet, fromNet)
}

func (s *Server) readAgentDecision(client, up net.Conn) (buffered []byte, session *agent.Session, id, runID, requestID string, helloRelayed, start bool) {
	deadline := func() { _ = client.SetReadDeadline(time.Now().Add(3 * time.Second)) }
	deadline()
	for {
		op, payload, raw, err := readWSFrame(client)
		if err != nil {
			return buffered, nil, "", runID, requestID, helloRelayed, false
		}
		if op == 0x9 {
			_ = writeWSFrame(client, 0xA, payload)
			if !helloRelayed {
				deadline()
			}
			continue
		}
		if (op != 0x1 && op != 0x2) || len(buffered)+len(raw) > 8<<20 {
			return append(buffered, raw...), nil, "", runID, requestID, helloRelayed, false
		}
		frame := protox.ClassifyAgentPayload(payload)
		if frame.Kind == "hello" {
			if !helloRelayed && up != nil {
				if _, err := up.Write(raw); err != nil {
					return buffered, nil, "", runID, requestID, false, false
				}
				if !relayServerData(up, client) {
					return buffered, nil, "", runID, requestID, false, false
				}
				helloRelayed = true
				// 客户端把这条连接放进池里，要等用户发送才发这一轮。这里再套短超时，超时后就会把整条连接交给官方。
				_ = client.SetReadDeadline(time.Time{})
			}
			continue
		}
		buffered = append(buffered, raw...)
		if frame.Kind == "meta" {
			if frame.RunID != "" {
				runID = frame.RunID
			}
			if frame.RequestID != "" {
				requestID = frame.RequestID
			}
			if !helloRelayed {
				deadline()
			}
			continue
		}
		if frame.Kind != "agent" || len(frame.Message) == 0 || s.hub == nil {
			// hello 之后模型还没出现。认不出的帧先留下，不能据此把整条连接交给官方。
			if helloRelayed && s.hub != nil {
				continue
			}
			return buffered, nil, "", runID, requestID, helloRelayed, false
		}
		var msg cursorpb.AgentClientMessage
		if proto.Unmarshal(frame.Message, &msg) != nil || msg.GetRunRequest() == nil {
			if helloRelayed {
				continue
			}
			return buffered, nil, "", runID, requestID, helloRelayed, false
		}
		route, session, start := s.hub.Client(frame.RequestID, &msg)
		slog.Debug("Agents", "model", route.ModelID, "where", where(session != nil))
		if session != nil {
			return buffered, session, frame.RequestID, runID, requestID, helloRelayed, start
		}
		return buffered, nil, "", runID, requestID, helloRelayed, false
	}
}

// relayServerData 把上游的下一帧数据交给客户端。握手的 hello 必须先回到客户端，它才会发出这一轮的模型。
func relayServerData(up, client net.Conn) bool {
	_ = up.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer up.SetReadDeadline(time.Time{})
	for {
		op, payload, raw, err := readWSFrame(up)
		if err != nil {
			return false
		}
		if op == 0x9 {
			_ = writeWSFrame(up, 0xA, payload)
			continue
		}
		if op != 0x1 && op != 0x2 {
			return false
		}
		_, err = client.Write(raw)
		return err == nil
	}
}

// serveLocalIdle 回答同一条已结束 run 上新开的连接。客户端会在回合结束后再发 resume，这条不能交给官方。
func (s *Server) serveLocalIdle(client net.Conn, runID, requestID string, helloRelayed bool, fromNet io.Writer) {
	if runID == "" {
		runID = requestID
	}
	if requestID == "" {
		requestID = runID
	}
	if !helloRelayed {
		_ = writeLogged(client, fromNet, []byte{0x0a, 0x02, 0x08, 0x01})
	}
	_ = writeLogged(client, fromNet, protox.EncodeAgentRunAccepted(runID, requestID))
	raw, err := proto.Marshal(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: &cursorpb.InteractionUpdate{
		Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}},
	}}})
	if err == nil {
		_ = writeLogged(client, fromNet, protox.EncodeAgentServer(runID, 0, raw))
	}
	for {
		op, payload, _, err := readWSFrame(client)
		if err != nil || op == 0x8 {
			return
		}
		if op == 0x9 {
			_ = writeWSFrame(client, 0xA, payload)
		}
	}
}

// serveLocalAttach 把后到的连接接到还在跑的本地会话上，不再另起一轮，也不交给官方。
func (s *Server) serveLocalAttach(client net.Conn, id, runID, requestID string, helloRelayed bool, toNet, fromNet io.Writer) {
	if runID == "" {
		runID = id
	}
	if requestID == "" {
		requestID = runID
	}
	if !helloRelayed {
		_ = writeLogged(client, fromNet, []byte{0x0a, 0x02, 0x08, 0x01})
	}
	_ = writeLogged(client, fromNet, protox.EncodeAgentRunAccepted(runID, requestID))
	for {
		op, payload, raw, err := readWSFrame(client)
		if err != nil || op == 0x8 {
			return
		}
		noteRaw(toNet, raw)
		if op == 0x9 {
			_ = writeWSFrame(client, 0xA, payload)
			continue
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
}

func (s *Server) serveLocalAgent(client net.Conn, session *agent.Session, id, runID, requestID string, helloRelayed bool, toNet, fromNet io.Writer) {
	s.countLocal()
	defer s.hub.Done(id)
	if runID == "" {
		runID = id
	}
	if requestID == "" {
		requestID = runID
	}
	var mu sync.Mutex
	var seq uint64
	emit := func(m *cursorpb.AgentServerMessage) error {
		raw, err := proto.Marshal(m)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		n := seq
		seq++
		// 客户端的序号从 0 起。从 1 起会被当成乱序丢掉，界面停在 Reconnecting。
		return writeLogged(client, fromNet, protox.EncodeAgentServer(runID, n, raw))
	}
	mu.Lock()
	if !helloRelayed {
		_ = writeLogged(client, fromNet, []byte{0x0a, 0x02, 0x08, 0x01})
	}
	_ = writeLogged(client, fromNet, protox.EncodeAgentRunAccepted(runID, requestID))
	mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d, err := dialer.ForModel(s.proxy(), session.Model.UseProxy)
		session.Proxy = s.proxy()
		session.Web = s.webDial
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
