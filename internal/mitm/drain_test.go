package mitm

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
)

// 交接后，Cursor 复用的 keep-alive 隧道要断开，下一次请求才会重连到新进程。
func TestDrainClosesIdleKeepAliveTunnels(t *testing.T) {
	s := &Server{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })}
	s.drain.addTunnel(srv)
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	if resp.Close {
		t.Fatal("keep-alive should be on before draining")
	}

	s.DrainIdle()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("idle tunnel was not closed")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("idle tunnel is still open after draining")
	}
}

// 池里预先连好、还没开始任何一轮的 Agents 连接要关掉；刚收到帧的可能正要发这一轮，留着。
func TestDrainClosesOnlyIdleWaitingAgentConns(t *testing.T) {
	s := &Server{}
	idleA, idleB := net.Pipe()
	busyA, busyB := net.Pipe()
	defer idleB.Close()
	defer busyA.Close()
	defer busyB.Close()
	var old, fresh lastFrame
	old.at.Store(time.Now().Add(-2 * waitingIdle).UnixNano())
	fresh.touch()
	s.drain.watch(idleA, old.quiet)
	s.drain.watch(busyA, fresh.quiet)

	if n := s.DrainIdle(); n != 1 {
		t.Fatalf("closed %d waiting conns, want 1", n)
	}
	_ = idleB.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := idleB.Read(make([]byte, 1)); err != io.EOF && err != io.ErrClosedPipe {
		t.Fatalf("idle pre-connect still open: %v", err)
	}
	go func() { _, _ = busyB.Write([]byte{1}) }()
	_ = busyA.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := busyA.Read(make([]byte, 1)); err != nil {
		t.Fatalf("active pre-connect was closed: %v", err)
	}
}

// 只有客户端真的发出过 run_request，才算这条连接上有官方回合；只有 meta 的连接要能被排空。
func TestStartedRunsNeedsARunRequest(t *testing.T) {
	run, _ := proto.Marshal(&cursorpb.AgentClientMessage{Message: &cursorpb.AgentClientMessage_RunRequest{RunRequest: &cursorpb.AgentRunRequest{}}})
	meta := wsFrameBytes(0x2, protox.EncodeAgentRunAccepted("run-1", "req-1"))
	agent := wsFrameBytes(0x2, protox.EncodeAgentServer("req-1", 0, run))
	if got := startedRuns(meta); len(got) != 0 {
		t.Fatalf("meta alone is not a run: %v", got)
	}
	got := startedRuns(append(append([]byte{}, meta...), agent...))
	if len(got) != 1 || got[0][0] != "req-1" {
		t.Fatalf("runs = %v", got)
	}
}

// 官方回合进行中的连接不能被排空打断；上游送回这一轮的 TurnEnded（用 run id 或 request id 都行）之后才算空闲。
func TestOfficialRunKeepsConnectionUntilTurnEnded(t *testing.T) {
	var runs runSet
	runs.start("run-1", "req-1")
	if !runs.busy() {
		t.Fatal("run should be live")
	}
	other, _ := proto.Marshal(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: &cursorpb.InteractionUpdate{
		Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: "hi"}},
	}}})
	if _, ok := turnEnded(protox.EncodeAgentServer("run-1", 0, other)); ok {
		t.Fatal("a text delta is not the end of the turn")
	}
	ended, _ := proto.Marshal(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: &cursorpb.InteractionUpdate{
		Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}},
	}}})
	id, ok := turnEnded(protox.EncodeAgentServer("req-1", 5, ended))
	if !ok || id != "req-1" {
		t.Fatalf("turn end not recognised: %q %v", id, ok)
	}
	runs.end(id)
	if runs.busy() {
		t.Fatal("run should be over")
	}
	runs.start("", "")
	if runs.busy() {
		t.Fatal("a run without ids must not stay busy")
	}
}
