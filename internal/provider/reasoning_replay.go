package provider

import (
	"encoding/json"
	"strings"
)

// reasoningReplayItem is one official Responses reasoning output item that can
// be sent back on the next turn. The item id must be the upstream id; a missing
// id is not replaced with a placeholder.
type reasoningReplayItem struct {
	ID      string `json:"id"`
	Enc     string `json:"encrypted_content"`
	Summary string `json:"summary,omitempty"`
}

// reasoningReplayEnvelope is stored in Message.ReasoningSignature for OpenAI
// Responses. The tagged shape is distinct from Anthropic thinking signatures
// and from older inner builds that stored raw encrypted_content in this field.
// Those older blobs must not be treated as a reasoning item id.
type reasoningReplayEnvelope struct {
	Items []reasoningReplayItem `json:"rs_items"`
}

func encodeReasoningReplay(items []reasoningReplayItem) string {
	out := make([]reasoningReplayItem, 0, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.ID)
		if id == "" || it.Enc == "" {
			continue
		}
		out = append(out, reasoningReplayItem{ID: id, Enc: it.Enc, Summary: it.Summary})
	}
	if len(out) == 0 {
		return ""
	}
	raw, err := json.Marshal(reasoningReplayEnvelope{Items: out})
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeReasoningReplay(sig string) []reasoningReplayItem {
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return nil
	}
	var env reasoningReplayEnvelope
	if err := json.Unmarshal([]byte(sig), &env); err != nil {
		return nil
	}
	if env.Items == nil {
		return nil
	}
	out := make([]reasoningReplayItem, 0, len(env.Items))
	for _, it := range env.Items {
		id := strings.TrimSpace(it.ID)
		if id == "" || it.Enc == "" {
			continue
		}
		out = append(out, reasoningReplayItem{ID: id, Enc: it.Enc, Summary: it.Summary})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *accumulator) ensureReasoning(id string, fresh bool) *reasoningReplayItem {
	id = strings.TrimSpace(id)
	if id != "" {
		for i := range a.reasoningReplay {
			if a.reasoningReplay[i].ID == id {
				return &a.reasoningReplay[i]
			}
		}
	}
	if !fresh && len(a.reasoningReplay) > 0 && id == "" {
		return &a.reasoningReplay[len(a.reasoningReplay)-1]
	}
	a.reasoningReplay = append(a.reasoningReplay, reasoningReplayItem{ID: id})
	return &a.reasoningReplay[len(a.reasoningReplay)-1]
}

func (a *accumulator) noteResponsesFinish(status, reason string, incomplete bool) {
	if incomplete {
		switch {
		case reason != "":
			a.finish = reason
		case status != "":
			a.finish = status
		default:
			a.finish = "incomplete"
		}
		return
	}
	if a.finish != "" && a.finish != "completed" && a.finish != "in_progress" {
		return
	}
	if status != "" {
		a.finish = status
	}
}
