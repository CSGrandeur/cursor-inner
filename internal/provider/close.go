package provider

const (
	interruptedTool = "Tool call was interrupted before it finished."
	interruptedTurn = "Operation interrupted."
)

// CloseDangling 补上没有结果的工具调用；若历史停在用户消息（取消发生在助手回复之前），
// 也补一条助手消息。否则下一轮再追加用户消息会变成连续两条 user，模型常先想上一条。
func CloseDangling(msgs []Message) []Message {
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
		rebuilt = append(rebuilt, Message{Role: "assistant", Content: interruptedTurn})
	}
	// Cancel before any assistant reply leaves a trailing user message.
	// The next turn would append another user and the model often reasons about the previous one.
	if rebuilt[len(rebuilt)-1].Role == "user" {
		rebuilt = append(rebuilt, Message{Role: "assistant", Content: interruptedTurn})
	}
	return rebuilt
}
