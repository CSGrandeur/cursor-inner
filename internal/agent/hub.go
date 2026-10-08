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

// Hub 按 request_id 把 BidiAppend 的客户端消息路由到对应的 Session。
type Hub struct {
	mu      sync.Mutex
	slots   map[string]*slot
	lookup  func(id string) (config.Model, bool)
	history *History
}

func New(lookup func(id string) (config.Model, bool), history *History) *Hub {
	return &Hub{slots: map[string]*slot{}, lookup: lookup, history: history}
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
	s := h.slotLocked(requestID)
	route := Route{RequestID: requestID}
	delivered := false
	switch m := msg.GetMessage().(type) {
	case *cursorpb.AgentClientMessage_RunRequest:
		route.ModelID = modelID(m.RunRequest)
		if s.decided {
			break
		}
		s.decided = true
		if route.ModelID != "" {
			if model, ok := h.lookup(route.ModelID); ok {
				s.session = newSession(requestID, model, m.RunRequest, h.history)
			}
		}
		close(s.ready)
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
	return route, nil
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

func (h *Hub) Done(requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.slots, requestID)
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
