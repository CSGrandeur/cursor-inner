package agent

import (
	"context"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

type Decision struct {
	Local    bool
	Model    config.Model
	Messages []provider.Message
}

type Hub struct {
	mu     sync.Mutex
	runs   map[string]*run
	lookup func(id string) (config.Model, bool)
}

type run struct {
	decided  bool
	local    bool
	model    config.Model
	messages []provider.Message
	ready    chan struct{}
	born     time.Time
}

func New(lookup func(id string) (config.Model, bool)) *Hub {
	return &Hub{runs: map[string]*run{}, lookup: lookup}
}

type Route struct {
	RequestID string
	ModelID   string
	Local     bool
}

func (h *Hub) Bidi(body []byte) (Route, error) {
	id, client, err := protox.DecodeBidi(body)
	if err != nil {
		return Route{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	slot := h.slotLocked(id)
	modelID := protox.ModelID(client)
	if modelID == "" {
		return Route{RequestID: id, Local: slot.decided && slot.local}, nil
	}
	model, ok := h.lookup(modelID)
	slot.decided = true
	slot.local = ok
	if ok {
		slot.model = model
		if len(slot.messages) == 0 {
			for _, message := range protox.ChatMessages(client) {
				slot.messages = append(slot.messages, provider.Message{Role: message.Role, Content: message.Content})
			}
		}
	}
	h.wake(slot)
	return Route{RequestID: id, ModelID: modelID, Local: ok}, nil
}

func (h *Hub) Wait(ctx context.Context, id string) (Decision, error) {
	h.mu.Lock()
	slot := h.slotLocked(id)
	if slot.decided {
		d := h.decisionLocked(slot)
		h.mu.Unlock()
		return d, nil
	}
	ready := slot.ready
	h.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.decisionLocked(h.runs[id]), nil
}

func (h *Hub) Done(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.runs, id)
}

func (h *Hub) slotLocked(id string) *run {
	slot, ok := h.runs[id]
	if !ok {
		slot = &run{ready: make(chan struct{}), born: time.Now()}
		h.runs[id] = slot
	}
	return slot
}

func (h *Hub) decisionLocked(slot *run) Decision {
	if slot == nil || !slot.decided {
		return Decision{}
	}
	return Decision{Local: slot.local, Model: slot.model, Messages: append([]provider.Message(nil), slot.messages...)}
}

func (h *Hub) wake(slot *run) {
	select {
	case <-slot.ready:
	default:
		close(slot.ready)
	}
}

func (h *Hub) sweepLocked() {
	now := time.Now()
	for id, slot := range h.runs {
		if now.Sub(slot.born) > 5*time.Minute {
			delete(h.runs, id)
		}
	}
}
