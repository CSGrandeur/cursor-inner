package provider

import (
	"strings"

	"cursor-inner/internal/config"
)

// ReasoningIssuer 标识推理签名的签发方：接口类型 + 地址 + 模型名。
// 不用模型 id：id 里含密钥和显示名，换 Key 或改名不该让同一模型的签名作废。
func ReasoningIssuer(m config.Model) string {
	return strings.Join([]string{
		strings.TrimSpace(m.Type),
		strings.TrimRight(strings.ToLower(strings.TrimSpace(m.BaseURL)), "/"),
		strings.TrimSpace(m.Model),
	}, "|")
}

func hasThoughtSignature(calls []ToolCall) bool {
	for _, c := range calls {
		if c.ThoughtSignature != "" {
			return true
		}
	}
	return false
}

// StripForeignReasoning 在把历史交给 m 之前，去掉别的模型签发的推理签名
// （Anthropic thinking signature、Responses encrypted_content、Gemini thought_signature）。
// 备用模型接手或对话中途换模型时，对方会拒收这些不透明的块，而且它们留在历史里会让之后每一轮都失败。
// 推理明文保留（有的接口要求回传 reasoning_content）。没记签发方的旧消息维持原样。
func StripForeignReasoning(m config.Model, messages []Message) []Message {
	self := ReasoningIssuer(m)
	var out []Message
	for i, message := range messages {
		if message.Role != "assistant" || message.ReasoningBy == "" || message.ReasoningBy == self {
			continue
		}
		if message.ReasoningSignature == "" && !hasThoughtSignature(message.ToolCalls) {
			continue
		}
		if out == nil {
			out = append([]Message(nil), messages...)
		}
		out[i].ReasoningSignature = ""
		if hasThoughtSignature(out[i].ToolCalls) {
			calls := append([]ToolCall(nil), out[i].ToolCalls...)
			for j := range calls {
				calls[j].ThoughtSignature = ""
			}
			out[i].ToolCalls = calls
		}
	}
	if out == nil {
		return messages
	}
	return out
}

// stripAllReasoningSignatures 在上游 400 明确拒收推理签名或加密推理块时，剥掉当前历史里的全部不透明签名以便重试。
func stripAllReasoningSignatures(messages []Message) ([]Message, bool) {
	var out []Message
	for i, message := range messages {
		if message.ReasoningSignature == "" && !hasThoughtSignature(message.ToolCalls) {
			continue
		}
		if out == nil {
			out = append([]Message(nil), messages...)
		}
		out[i].ReasoningSignature = ""
		if hasThoughtSignature(out[i].ToolCalls) {
			calls := append([]ToolCall(nil), out[i].ToolCalls...)
			for j := range calls {
				calls[j].ThoughtSignature = ""
			}
			out[i].ToolCalls = calls
		}
	}
	if out == nil {
		return messages, false
	}
	return out, true
}
