package mitm

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
)

// drainState 记下交接后旧进程手里还能再接新一轮的连接。
// Release 只关监听：Cursor 复用的 keep-alive 隧道和池里的 Agents 连接还在旧进程上，
// 下一轮会照旧落到旧版本（2026-10-27：热替换 4 分钟后新对话仍报旧版本的错误）。
type drainState struct {
	on      atomic.Bool
	mu      sync.Mutex
	tunnels map[*http.Server]struct{}
	agents  map[net.Conn]func(time.Time) bool // Agents 连接和“现在没有回合在跑”的判断
}

// waitingIdle 是连接空闲多久才算没人在用。刚收到帧的连接可能正要发这一轮。
const waitingIdle = time.Second

func (d *drainState) addTunnel(srv *http.Server) {
	d.mu.Lock()
	if d.tunnels == nil {
		d.tunnels = map[*http.Server]struct{}{}
	}
	d.tunnels[srv] = struct{}{}
	d.mu.Unlock()
	if d.on.Load() {
		srv.SetKeepAlivesEnabled(false)
	}
}

func (d *drainState) dropTunnel(srv *http.Server) {
	d.mu.Lock()
	delete(d.tunnels, srv)
	d.mu.Unlock()
}

// watch 登记一条 Agents 连接。idle 返回真时，排空可以关掉它：上面没有回合在跑，下一轮会重连到新进程。
func (d *drainState) watch(c net.Conn, idle func(time.Time) bool) {
	d.mu.Lock()
	if d.agents == nil {
		d.agents = map[net.Conn]func(time.Time) bool{}
	}
	d.agents[c] = idle
	d.mu.Unlock()
}

func (d *drainState) unwatch(c net.Conn) {
	d.mu.Lock()
	delete(d.agents, c)
	d.mu.Unlock()
}

// sweep 关掉空闲的 keep-alive 隧道和空闲的 Agents 连接，返回关掉的 Agents 连接数。
// 隧道上正在返回的响应会带 Connection: close 结束；有回合在跑的连接不动。
func (d *drainState) sweep(now time.Time) int {
	d.mu.Lock()
	tunnels := make([]*http.Server, 0, len(d.tunnels))
	for srv := range d.tunnels {
		tunnels = append(tunnels, srv)
	}
	var idle []net.Conn
	for c, isIdle := range d.agents {
		if isIdle(now) {
			idle = append(idle, c)
			delete(d.agents, c)
		}
	}
	d.mu.Unlock()
	for _, srv := range tunnels {
		srv.SetKeepAlivesEnabled(false)
	}
	for _, c := range idle {
		_ = c.Close()
	}
	return len(idle)
}

// lastFrame 记录一条连接最后一次收发帧的时间。
type lastFrame struct{ at atomic.Int64 }

func (l *lastFrame) touch() { l.at.Store(time.Now().UnixNano()) }
func (l *lastFrame) quiet(now time.Time) bool {
	return now.Sub(time.Unix(0, l.at.Load())) >= waitingIdle
}

// runSet 记一条连接上正在跑的官方回合。同一轮会用 run id 和 request id 两个名字出现，任何一个收到结束就算结束。
type runSet struct {
	mu   sync.Mutex
	next int
	keys map[string]int
	live map[int]struct{}
}

func (r *runSet) start(ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys, r.live = map[string]int{}, map[int]struct{}{}
	}
	r.next++
	marked := false
	for _, id := range ids {
		if id != "" {
			r.keys[id] = r.next
			marked = true
		}
	}
	if marked {
		r.live[r.next] = struct{}{}
	}
}

func (r *runSet) end(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := r.keys[id]
	if !ok {
		return
	}
	delete(r.live, tok)
	for key, t := range r.keys {
		if t == tok {
			delete(r.keys, key)
		}
	}
}

func (r *runSet) busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.live) > 0
}

// startedRuns 从判定前缓存的客户端帧里找出真正发出的 run_request，返回这些轮的 id。
// 没有 run_request 就不算有官方回合在跑，否则这条连接永远不会被排空。
func startedRuns(buffered []byte) [][]string {
	var out [][]string
	r := bytes.NewReader(buffered)
	for r.Len() > 0 {
		op, payload, _, err := readWSFrame(r)
		if err != nil {
			break
		}
		if op != 0x1 && op != 0x2 {
			continue
		}
		frame := protox.ClassifyAgentPayload(payload)
		if frame.Kind != "agent" || len(frame.Message) == 0 {
			continue
		}
		var msg cursorpb.AgentClientMessage
		if proto.Unmarshal(frame.Message, &msg) == nil && msg.GetRunRequest() != nil {
			out = append(out, []string{frame.RequestID, frame.RunID})
		}
	}
	return out
}

// turnEnded 认出上游这一帧是不是某一轮的结束，返回那一轮的 id。
func turnEnded(payload []byte) (string, bool) {
	frame := protox.ClassifyAgentPayload(payload)
	if frame.Kind != "agent" || len(frame.Message) == 0 {
		return "", false
	}
	var msg cursorpb.AgentServerMessage
	if proto.Unmarshal(frame.Message, &msg) != nil || msg.GetInteractionUpdate().GetTurnEnded() == nil {
		return "", false
	}
	return frame.RequestID, true
}

// DrainIdle 在交接确认之后调用：本进程不再接新的一轮，Cursor 手里复用的连接断开后会重连到新进程。
// 不能在 Release 时就做：那时新进程还没接过端口，断开的连接重连会被拒绝。
func (s *Server) DrainIdle() int {
	s.drain.on.Store(true)
	n := s.drain.sweep(time.Now())
	if n > 0 {
		slog.Info("交接后关掉空闲的 Agents 连接，下一轮会连到新进程", "count", n)
	}
	return n
}
