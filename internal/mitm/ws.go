package mitm

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
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
		if resp.StatusCode == http.StatusUnauthorized && len(s.entries()) > 0 {
			// 只在官方未登录（401）时接管：限流、5xx 等要让客户端拿到官方原始响应，按自己的逻辑重试或提示。
			// 官方拒绝升级（未登录）：本地还有自定义模型时别让客户端拿到官方 401。
			// 给客户端一个本地 101，让它继续发这一轮；模型再按帧读——自定义模型就在本机回答。
			_ = up.Close()
			raw := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: " + websocketAccept(r.Header.Get("Sec-WebSocket-Key"))
			if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
				raw += "\r\nSec-WebSocket-Protocol: " + proto
			}
			raw += "\r\n\r\n"
			if _, err := client.Write([]byte(raw)); err != nil {
				_ = client.Close()
				return
			}
			s.noteWebSocket(r, http.StatusSwitchingProtocols, nil)
			var toNet, fromNet io.WriteCloser
			if open := currentRawOpen(); open != nil {
				toNet, fromNet = open(addr, "websocket")
			}
			s.steerWebSocket(client, nil, toNet, fromNet)
			return
		}
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
	if up != nil {
		defer up.Close()
	}
	defer closeRaw(toNet)
	defer closeRaw(fromNet)
	buffered, local, localID, runID, requestID, helloRelayed, start := s.readAgentDecision(client, up)
	_ = client.SetReadDeadline(time.Time{})
	if up != nil {
		_ = up.SetReadDeadline(time.Time{})
	}
	if local != nil {
		if up != nil {
			_ = up.Close()
		}
		noteRaw(toNet, buffered)
		if start {
			s.serveLocalAgent(client, local, localID, runID, requestID, helloRelayed, toNet, fromNet)
		} else if local.Finished() && local.HasQueuedInput() {
			// 这条 run 结束之后又收到了后台任务结果这类输入（见 Hub 的 finished 分支）。
			// 客户端 resume 它是来取这一轮的结果的：接着跑一轮，只回放旧状态它只会报 couldn't resume。
			slog.Debug("Agents resume，有新的输入，接着跑一轮", "request", localID, "run", runID)
			s.serveLocalAgent(client, local, localID, runID, requestID, helloRelayed, toNet, fromNet)
		} else if local.Finished() {
			s.serveLocalIdle(client, local, runID, requestID, helloRelayed, fromNet)
		} else {
			s.serveLocalAttach(client, localID, runID, requestID, helloRelayed, toNet, fromNet)
		}
		return
	}
	s.watchOfficial(client, up, buffered, runID, requestID, toNet, fromNet)
}

// watchOfficial 把已经判定为官方的连接继续对拼，但后到的每一轮仍要看模型。
// 新的 run 是自定义模型时，这一轮和它前面还没送出的 meta 留在本机，不写给上游。
// 已经在走官方的轮次继续原样转发。正文里出现过模型 id 不算这一轮要本地回答。
func (s *Server) watchOfficial(client, up net.Conn, buffered []byte, runID, requestID string, toNet, fromNet io.WriteCloser) {
	if up == nil {
		// 官方拒绝升级（未登录等）且这一轮不是自定义模型：没有上游可转，只能让客户端知道失败了。
		slog.Debug("官方不可用且不是本地模型，断开连接", "request", requestID)
		return
	}
	if id := leakedModel(buffered, s.entries()); id != "" {
		slog.Error("自定义模型的对话没有留在本机，已断开，不交给官方", "model", id)
		return
	}
	var upMu, clientMu sync.Mutex
	writeUp := func(p []byte) error {
		if len(p) == 0 {
			return nil
		}
		upMu.Lock()
		defer upMu.Unlock()
		noteRaw(toNet, p)
		_, err := up.Write(p)
		return err
	}
	writeClient := func(p []byte) error {
		if len(p) == 0 {
			return nil
		}
		clientMu.Lock()
		defer clientMu.Unlock()
		noteRaw(fromNet, p)
		_, err := client.Write(p)
		return err
	}
	if err := writeUp(buffered); err != nil {
		return
	}
	go func() {
		for {
			op, payload, raw, err := readWSFrame(up)
			if err != nil {
				return
			}
			if op == 0x9 {
				pong := wsFrameBytes(0xA, payload)
				_ = writeUp(pong)
				continue
			}
			if op == 0x8 {
				return
			}
			if err := writeClient(raw); err != nil {
				return
			}
		}
	}()
	held := map[string]*heldFrames{}
	metaReq := map[string]string{}
	localRun := map[string]struct{}{}
	officialRun := map[string]struct{}{}
	markRun(officialRun, runID, requestID)
	var cancelLocal []context.CancelFunc
	defer func() {
		for _, cancel := range cancelLocal {
			cancel()
		}
	}()
	for {
		op, payload, raw, err := readWSFrame(client)
		if err != nil {
			return
		}
		if op == 0x8 {
			return
		}
		if op == 0x9 {
			if err := writeUp(raw); err != nil {
				_ = writeClient(wsFrameBytes(0xA, payload))
			}
			continue
		}
		if op != 0x1 && op != 0x2 {
			_ = writeUp(raw)
			continue
		}
		frame := protox.ClassifyAgentPayload(payload)
		switch frame.Kind {
		case "meta":
			if runMarked(localRun, frame) {
				continue
			}
			if runMarked(officialRun, frame) {
				_ = writeUp(raw)
				continue
			}
			if frame.RunID != "" && frame.RequestID != "" {
				metaReq[frame.RunID] = frame.RequestID
			}
			stashHeld(held, frame, raw)
		case "agent":
			if len(frame.Message) == 0 || s.hub == nil {
				_ = writeUp(raw)
				continue
			}
			var msg cursorpb.AgentClientMessage
			if proto.Unmarshal(frame.Message, &msg) != nil {
				_ = writeUp(raw)
				continue
			}
			id := frame.RequestID
			if id == "" {
				id = frame.RunID
			}
			route, session, start := s.hub.Client(id, &msg)
			if msg.GetRunRequest() != nil {
				slog.Debug("Agents", "model", route.ModelID, "where", where(session != nil))
			}
			if session != nil && start {
				ackRun, ackReq := ackIDs(id, frame, metaReq)
				markRun(localRun, id, ackRun, ackReq)
				dropHeld(held, frame)
				cancelLocal = append(cancelLocal, s.serveLocalBeside(writeClient, session, id, ackRun, ackReq))
				continue
			}
			if session != nil || route.Local {
				markRun(localRun, id, frame.RunID, frame.RequestID)
				dropHeld(held, frame)
				continue
			}
			markRun(officialRun, id, frame.RunID, frame.RequestID)
			for _, earlier := range dropHeld(held, frame) {
				_ = writeUp(earlier)
			}
			_ = writeUp(raw)
		default:
			_ = writeUp(raw)
		}
	}
}

type heldFrames struct {
	raws [][]byte
}

func stashHeld(held map[string]*heldFrames, frame protox.AgentPayload, raw []byte) {
	keys := frameKeys(frame)
	var box *heldFrames
	for _, key := range keys {
		if held[key] != nil {
			box = held[key]
			break
		}
	}
	if box == nil {
		box = &heldFrames{}
	}
	box.raws = append(box.raws, raw)
	for _, key := range keys {
		held[key] = box
	}
}

func dropHeld(held map[string]*heldFrames, frame protox.AgentPayload) [][]byte {
	var box *heldFrames
	for _, key := range frameKeys(frame) {
		if held[key] != nil {
			box = held[key]
			break
		}
	}
	if box == nil {
		return nil
	}
	for key, value := range held {
		if value == box {
			delete(held, key)
		}
	}
	return box.raws
}

func ackIDs(id string, frame protox.AgentPayload, metaReq map[string]string) (runID, requestID string) {
	runID = frame.RunID
	if runID == "" {
		runID = id
	}
	requestID = metaReq[runID]
	if requestID == "" {
		requestID = frame.RequestID
	}
	if requestID == "" {
		requestID = runID
	}
	return runID, requestID
}

func frameKeys(frame protox.AgentPayload) []string {
	var keys []string
	if frame.RunID != "" {
		keys = append(keys, frame.RunID)
	}
	if frame.RequestID != "" && frame.RequestID != frame.RunID {
		keys = append(keys, frame.RequestID)
	}
	return keys
}

func markRun(set map[string]struct{}, ids ...string) {
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
}

func runMarked(set map[string]struct{}, frame protox.AgentPayload) bool {
	for _, key := range frameKeys(frame) {
		if _, ok := set[key]; ok {
			return true
		}
	}
	return false
}

// serveLocalBeside 在还连着官方的那条连接上回答后到的自定义模型。hello 已经完成，不再发第二次。
func (s *Server) serveLocalBeside(writeClient func([]byte) error, session *agent.Session, id, runID, requestID string) context.CancelFunc {
	s.countLocal()
	if runID == "" {
		runID = id
	}
	if requestID == "" {
		requestID = runID
	}
	var seq uint64
	var seqMu sync.Mutex
	emit := func(m *cursorpb.AgentServerMessage) error {
		raw, err := proto.Marshal(m)
		if err != nil {
			return err
		}
		seqMu.Lock()
		n := seq
		seq++
		seqMu.Unlock()
		return writeClient(wsFrameBytes(0x2, protox.EncodeAgentServer(runID, n, raw)))
	}
	_ = writeClient(wsFrameBytes(0x2, protox.EncodeAgentRunAccepted(runID, requestID)))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer s.hub.Done(id)
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
	return cancel
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
			if !helloRelayed {
				if up != nil {
					// 官方已经升级成功：hello 只能来自官方。不要超时后伪造本地 hello——
					// 官方 hello 晚到时客户端会收到两个 hello，官方模型这一轮就乱了。
					if _, err := up.Write(raw); err != nil || !relayServerData(up, client) {
						return buffered, nil, "", runID, requestID, false, false
					}
					helloRelayed = true
				} else {
					// 官方拒绝升级，没有上游：直接本地 hello，客户端不再等官方。
					if err := writeWSFrame(client, 0x2, localHello()); err != nil {
						return buffered, nil, "", runID, requestID, false, false
					}
					helloRelayed = true
				}
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
// 回放最终状态（blobs + checkpoint + turn_ended），客户端才认「任务已完成」。
func (s *Server) serveLocalIdle(client net.Conn, session *agent.Session, runID, requestID string, helloRelayed bool, fromNet io.Writer) {
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
	var seq uint64
	if session != nil {
		slog.Debug("Agents resume，回放最终状态", "request", requestID, "run", runID)
		if frames, err := session.Replay(); err == nil {
			for _, m := range frames {
				raw, merr := proto.Marshal(m)
				if merr != nil {
					break
				}
				_ = writeLogged(client, fromNet, protox.EncodeAgentServer(runID, seq, raw))
				seq++
			}
		}
	}
	raw, err := proto.Marshal(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: &cursorpb.InteractionUpdate{
		Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}},
	}}})
	if err == nil {
		_ = writeLogged(client, fromNet, protox.EncodeAgentServer(runID, seq, raw))
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

// websocketAccept 按 RFC 6455 计算 Sec-WebSocket-Accept，用于本地接管官方拒绝的升级。
// localHello 是服务器 hello 帧：protobuf { field1: { field1: 1 } }（协议版本 1）。
// 官方 hello 拿不到时（未登录、断连）用它回给客户端，客户端才会继续发 run。
func localHello() []byte { return []byte{0x0a, 0x02, 0x08, 0x01} }

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
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
