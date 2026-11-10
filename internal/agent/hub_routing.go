package agent

import (
	"log/slog"
	"time"

	"cursor-inner/internal/cursorpb"
)

// Original ids identify transport lineage, not Session ownership. Only a run
// request may restore an unbound route. I/O never adopts or loads from disk.
func (h *Hub) applyOriginalLocked(id, original string, msg *cursorpb.AgentClientMessage) (Route, *Session, bool) {
	if original == "" {
		if alias := h.aliases[id]; alias != nil {
			original = alias.session.RequestID
		}
	}
	if original == "" || original == id {
		return h.applyLocked(id, msg)
	}
	target := h.slots[id]
	var current, source *Session
	if target != nil {
		current = target.session
	}
	if old := h.slots[original]; old != nil {
		source = old.session
	}
	if source == nil {
		if alias := h.aliases[id]; alias != nil && alias.session.RequestID == original {
			source = alias.session
		}
	}
	if run := msg.GetRunRequest(); run != nil {
		// A new user turn is not a replay of the old run, even with an original header.
		if current == nil && (target == nil || !target.decided) && run.GetAction().GetUserMessageAction() == nil {
			if f := h.finished[original]; f != nil {
				source = f.session
			}
			if source == nil {
				source = h.sessionFromDiskLocked(original)
			}
			// Reconnects to a live run may link an empty transport slot, but must
			// never adopt new run state or create a second model consumer.
			if source != nil && !source.Finished() && resumeOnly(run.GetAction()) && (run.GetConversationId() == "" || run.GetConversationId() == source.ConversationID()) && (modelID(run) == "" || modelID(run) == source.Model.ID) {
				slot := h.slotLocked(id)
				slot.session = source
				if !slot.decided {
					slot.decided = true
					close(slot.ready)
				}
				return Route{RequestID: id, ModelID: modelID(run), Local: true}, source, false
			}
			compatible := source != nil && !source.Running() && source.Finished()
			if compatible && run.GetConversationId() != "" && run.GetConversationId() != source.ConversationID() {
				compatible = false
			}
			if compatible && modelID(run) != "" && modelID(run) != source.Model.ID {
				compatible = false
				if source.Model.ID == "" && h.lookup != nil {
					if model, ok := h.lookup(modelID(run)); ok {
						source.Model = model
						compatible = true
					}
				}
			}
			if compatible {
				h.noteResumeLocked(original, id)
			}
		}
		return h.applyLocked(id, msg)
	}
	// A known official route cannot be captured by an unrelated original header.
	if target != nil && target.decided && current == nil {
		return Route{RequestID: id}, nil, false
	}
	if exec := msg.GetExecClientMessage(); exec != nil {
		owner := current
		if owner == nil || !owner.ownsExec(exec) {
			owner = nil
			if source != nil && source.ownsExec(exec) {
				owner = source
			}
		}
		local := current != nil || source != nil || h.finished[original] != nil || (h.history != nil && h.history.LoadRun(original) != "")
		sessionID := ""
		if owner != nil {
			sessionID = owner.RequestID
			if current == nil {
				h.aliases[id] = &finished{session: owner, at: time.Now()}
			}
			owner.deliver(exec)
		}
		slog.Debug("exec routed", "request", id, "original", original, "session", sessionID, "id", exec.GetId(), "exec", exec.GetExecId(), "delivered", owner != nil)
		return Route{RequestID: id, Local: local}, current, false
	}
	// Heartbeats carry no execution input and must not change any binding.
	if msg.GetClientHeartbeat() != nil {
		return Route{RequestID: id, Local: current != nil}, current, false
	}
	if current != nil {
		return h.applyLocked(id, msg)
	}
	if source != nil {
		switch {
		case msg.GetInteractionResponse() != nil && !source.Finished():
			source.deliverInteraction(msg.GetInteractionResponse())
		case msg.GetKvClientMessage() != nil && !source.Finished():
			source.deliverKV(msg.GetKvClientMessage())
		case msg.GetConversationAction() != nil:
			source.deliverAction(msg.GetConversationAction())
		default:
			return h.applyLocked(id, msg)
		}
		return Route{RequestID: id, Local: true}, nil, false
	}
	// A post-turn action can be queued on the finished source without binding the
	// new request. RunRequest will later restore it and adopt its action exactly once.
	if msg.GetConversationAction() != nil {
		if f := h.finished[original]; f != nil {
			source = f.session
		} else {
			source = h.sessionFromDiskLocked(original)
		}
		if source != nil {
			source.deliverAction(msg.GetConversationAction())
			return Route{RequestID: id, Local: true}, nil, false
		}
	}
	return h.applyLocked(id, msg)
}

func (s *Session) ownsExec(msg *cursorpb.ExecClientMessage) bool {
	if s.Finished() {
		return false
	}
	value, ok := s.execs.Load(msg.GetId())
	return ok && (msg.GetExecId() == "" || value.(string) == msg.GetExecId())
}
