package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
)

// Route 是一条 BidiAppend 的分流结论。
type Route struct {
	RequestID string
	ModelID   string
	Local     bool
}

type slot struct {
	session *Session
	decided bool
	ready   chan struct{}
	born    time.Time
}

type finished struct {
	session *Session
	at      time.Time
}

// Hub 按 request_id 把 BidiAppend 的客户端消息路由到对应的 Session。
type Hub struct {
	mu       sync.Mutex
	slots    map[string]*slot
	finished map[string]*finished
	lookup   func(id string) (config.Model, bool)
	history  *History
}

func New(lookup func(id string) (config.Model, bool), history *History) *Hub {
	return &Hub{slots: map[string]*slot{}, finished: map[string]*finished{}, lookup: lookup, history: history}
}

// Bidi 处理一条已解压的 BidiAppend 请求体。
func (h *Hub) Bidi(body []byte) (Route, error) {
	requestID, raw, err := protox.DecodeBidi(body)
	if err != nil {
		return Route{}, err
	}
	var msg cursorpb.AgentClientMessage
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return Route{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	route, _, _ := h.applyLocked(requestID, &msg)
	return route, nil
}

// Client 处理 Agents 窗口 WebSocket 里的一条客户端消息。
// start 只在这一次新建了本地会话时为真，调用方据此开始 Run。
func (h *Hub) Client(requestID string, msg *cursorpb.AgentClientMessage) (Route, *Session, bool) {
	if msg == nil || requestID == "" {
		return Route{}, nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	return h.applyLocked(requestID, msg)
}

func (h *Hub) applyLocked(requestID string, msg *cursorpb.AgentClientMessage) (Route, *Session, bool) {
	s := h.slotLocked(requestID)
	route := Route{RequestID: requestID}
	start := false
	delivered := false
	switch m := msg.GetMessage().(type) {
	case *cursorpb.AgentClientMessage_RunRequest:
		route.ModelID = modelID(m.RunRequest)
		// 模型可能不在这条 run 的第一条消息里。先到的消息认不出，不能让后面带自定义模型的消息改走官方。
		if s.session == nil && route.ModelID != "" && h.lookup != nil {
			// 同一条 run 的 resume（回合结束后约 90 秒客户端会重发）：接到已经答完的会话上，
			// 不再新建会话调一次模型。新建会得到不同的输出，把用户已经看到的思考和回答覆盖掉。
			if f := h.finished[requestID]; f != nil {
				s.session = f.session
			} else if model, ok := h.lookup(route.ModelID); ok {
				s.session = newSession(requestID, model, m.RunRequest, h.history)
				for _, id := range model.Fallback {
					if fb, ok := h.lookup(id); ok && fb.ID != model.ID {
						s.session.Fallbacks = append(s.session.Fallbacks, fb)
					}
				}
				start = true
			}
		}
		if !s.decided {
			s.decided = true
			close(s.ready)
		}
	case *cursorpb.AgentClientMessage_ExecClientMessage:
		if s.session != nil {
			s.session.deliver(m.ExecClientMessage)
		}
	case *cursorpb.AgentClientMessage_InteractionResponse:
		if s.session != nil {
			s.session.deliverInteraction(m.InteractionResponse)
		}
	case *cursorpb.AgentClientMessage_ConversationAction:
		session := s.session
		if session == nil {
			if id := actionRunID(m.ConversationAction); id != "" {
				if other := h.slots[id]; other != nil {
					session = other.session
				}
			}
		}
		if session != nil {
			session.deliverAction(m.ConversationAction)
			delivered = true
		}
	case *cursorpb.AgentClientMessage_KvClientMessage:
		if s.session != nil {
			s.session.deliverKV(m.KvClientMessage)
		}
	}
	route.Local = s.session != nil || delivered
	return route, s.session, start
}

func actionRunID(action *cursorpb.ConversationAction) string {
	if action == nil {
		return ""
	}
	return action.GetInjectContextAction().GetExpectedRunId()
}

// Wait 等 BidiAppend 判定完成，返回本地 Session；走官方时返回 nil。
func (h *Hub) Wait(ctx context.Context, requestID string) (*Session, error) {
	h.mu.Lock()
	s := h.slotLocked(requestID)
	ready := s.ready
	h.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return s.session, nil
}

// Active 是正在本进程里跑的自定义模型会话数。
func (h *Hub) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.slots {
		if s.session != nil && time.Since(s.born) < 2*time.Hour {
			n++
		}
	}
	return n
}

func (h *Hub) Done(requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slots[requestID]
	delete(h.slots, requestID)
	if s != nil && s.session != nil && s.session.Finished() {
		// 已答完的 run 保留一段时间：客户端会在回合结束后再用同一条 run 发 resume，
		// 或是带 X-Original-Request-Id 重新开一条请求。见 resumeFinished / runSSE 的恢复路径。
		h.finished[requestID] = &finished{session: s.session, at: time.Now()}
	}
}

// Finished 报告 requestID 是不是本机已经答完的一条 run。
func (h *Hub) Finished(requestID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.finished[requestID]
	return f != nil
}

// Owns 报告 requestID 是不是本机的会话（正在本地回答，或本地已经答完）。
// 本机代为回答的官方请求（心跳、resume、握手）必须先用它确认归属；官方的 run 一律原样转发。
func (h *Hub) Owns(requestID string) bool {
	if requestID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished[requestID] != nil {
		return true
	}
	s := h.slots[requestID]
	return s != nil && s.session != nil
}

// NoteResume 把一条带 X-Original-Request-Id 的 resume 接到 oldID 已经答完的会话上。
// 返回 false 表示这条 original 不是本机答完的 run，调用方按原流程处理。
func (h *Hub) NoteResume(oldID, newID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var sess *Session
	if f := h.finished[oldID]; f != nil {
		sess = f.session
	} else if s := h.slots[oldID]; s != nil && s.session != nil && s.session.Finished() {
		// WS 连接还开在池里时 serveLocalAgent 还没走到 Done，会话留在 slots 里。
		sess = s.session
	}
	if sess == nil {
		return false
	}
	if newID == "" {
		newID = oldID
	}
	ready := make(chan struct{})
	close(ready)
	h.slots[newID] = &slot{session: sess, decided: true, ready: ready, born: time.Now()}
	return true
}

func (h *Hub) slotLocked(id string) *slot {
	s, ok := h.slots[id]
	if !ok {
		s = &slot{ready: make(chan struct{}), born: time.Now()}
		h.slots[id] = s
	}
	return s
}

func (h *Hub) sweepLocked() {
	for id, s := range h.slots {
		if time.Since(s.born) > 6*time.Hour {
			delete(h.slots, id)
		}
	}
	for id, f := range h.finished {
		if time.Since(f.at) > 2*time.Hour {
			delete(h.finished, id)
		}
	}
}

func modelID(run *cursorpb.AgentRunRequest) string {
	id := run.GetRequestedModel().GetModelId()
	if id == "" {
		id = run.GetModelDetails().GetModelId()
	}
	if i := strings.IndexByte(id, '['); i > 0 {
		id = id[:i]
	}
	return id
}
