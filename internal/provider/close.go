package provider

import (
	"context"
	"errors"
	"strings"
)

const (
	interruptedTool = "Tool call was interrupted before it finished."
	interruptedTurn = "Operation interrupted."
)

// CloseReason is the assistant line written into the checkpoint when a turn ends
// with dangling tools or a trailing user. Cursor redraws from the checkpoint, so
// this text is the UI ending: cancel -> 已停止, upstream error -> Explain(err).
// Do not collapse everything to Operation interrupted. (2026-10-28 aly cancel/503).
func CloseReason(err error) string {
	if err == nil {
		return interruptedTurn
	}
	if s := strings.TrimSpace(Explain(err)); s != "" {
		return s
	}
	if errors.Is(err, context.Canceled) {
		return "已停止"
	}
	return interruptedTurn
}

// CloseDangling fills missing tool results; if history ends on a user message
// (cancel before any assistant reply), it also appends an assistant line.
func CloseDangling(msgs []Message) []Message {
	return CloseDanglingWith(msgs, interruptedTurn)
}

// CloseDanglingWith is CloseDangling with a custom closing assistant message.
// Pause turns must close with the pause note: the last assistant message in the
// checkpoint is what Cursor redraws as the ending.
func CloseDanglingWith(msgs []Message, closing string) []Message {
	if len(msgs) == 0 {
		return msgs
	}
	rebuilt := make([]Message, 0, len(msgs))
	for i := 0; i < len(msgs); i++ {
		message := msgs[i]
		rebuilt = append(rebuilt, message)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		have := map[string]bool{}
		j := i + 1
		for j < len(msgs) && msgs[j].Role == "tool" {
			have[msgs[j].ToolCallID] = true
			rebuilt = append(rebuilt, msgs[j])
			j++
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || have[call.ID] {
				continue
			}
			rebuilt = append(rebuilt, Message{Role: "tool", ToolCallID: call.ID, Content: interruptedTool, IsError: true})
		}
		i = j - 1
	}
	if rebuilt[len(rebuilt)-1].Role == "tool" {
		rebuilt = append(rebuilt, Message{Role: "assistant", Content: closing})
	}
	if rebuilt[len(rebuilt)-1].Role == "user" {
		rebuilt = append(rebuilt, Message{Role: "assistant", Content: closing})
	}
	return rebuilt
}
