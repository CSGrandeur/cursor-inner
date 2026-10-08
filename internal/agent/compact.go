package agent

import (
	"context"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

// compactMessages 只在估算超过窗口的七成时缩短较早的工具输出。没到线就原样保留，避免打乱前缀。
func compactMessages(messages []provider.Message, window int) []provider.Message {
	if window <= 0 {
		window = 128000
	}
	if estimateTokens(messages) <= window*7/10 {
		return messages
	}
	out := append([]provider.Message(nil), messages...)
	cutoff := len(out) - 6
	if cutoff < 1 {
		cutoff = 1
	}
	for i := 1; i < cutoff; i++ {
		if out[i].Role != "tool" || len(out[i].Content) < 800 {
			continue
		}
		body := out[i].Content
		out[i].Content = body[:300] + "\n…[earlier tool output compacted]…\n" + body[len(body)-200:]
	}
	return out
}

// forceCompact 在接口报上下文超长时使用：先缩短工具输出，仍然原样就只留下最近几条。
func overBudget(messages []provider.Message, window int) bool {
	if window <= 0 {
		window = 128000
	}
	return estimateTokens(messages) > window*7/10
}

func forceCompact(messages []provider.Message) []provider.Message {
	clipped := compactMessages(messages, 1)
	if estimateTokens(clipped) < estimateTokens(messages) {
		return clipped
	}
	if len(messages) <= 4 {
		return clipped
	}
	return append([]provider.Message{{Role: "user", Content: "Earlier messages were removed after the context window was exceeded."}}, recentTail(messages, 4)...)
}

// recentTail 返回最后 n 条消息。切点落在 tool 消息上时向前挪到对应的助手消息，
// 保证每条工具结果都带着它的工具调用。
func recentTail(messages []provider.Message, n int) []provider.Message {
	start := max(len(messages)-n, 0)
	for start > 0 && messages[start].Role == "tool" {
		start--
	}
	return append([]provider.Message(nil), messages[start:]...)
}

func chatOrCompact(ctx context.Context, model config.Model, dial dialer.Func, system string, messages []provider.Message, tools []provider.Tool, onText func(string) error, onThinking func(string) error) ([]provider.Message, provider.Message, error) {
	reply, err := provider.Chat(ctx, model, dial, system, messages, tools, onText, onThinking)
	if !provider.Overflow(err) {
		return messages, reply, err
	}
	messages = forceCompact(messages)
	reply, err = provider.Chat(ctx, model, dial, system, messages, tools, onText, onThinking)
	return messages, reply, err
}

func estimateTokens(messages []provider.Message) int {
	base := 0
	from := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].PromptTokens > 0 {
			base = messages[i].PromptTokens
			from = i
			break
		}
	}
	total := base
	for _, message := range messages[from:] {
		total += len(message.Content) / 4
		total += len(message.Reasoning) / 4
	}
	return total
}
