package provider

const (
	interruptedTool = "Tool call was interrupted before it finished."
	interruptedTurn = "Operation interrupted."
)

// CloseDangling 补上没有结果的工具调用。历史如果以 tool 消息结尾，再补一条助手消息，
// 避免下一轮用户消息紧跟在 tool 后面。
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
	return rebuilt
}
