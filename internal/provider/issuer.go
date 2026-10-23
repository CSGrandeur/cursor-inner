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

// StripForeignReasoning 在把历史交给 m 之前，去掉别的模型签发的推理签名
// （Anthropic thinking signature、Responses encrypted_content）。
// 备用模型接手或对话中途换模型时，对方会拒收这些不透明的块，而且它们留在历史里会让之后每一轮都失败。
// 推理明文保留（有的接口要求回传 reasoning_content）。没记签发方的旧消息维持原样。
func StripForeignReasoning(m config.Model, messages []Message) []Message {
	self := ReasoningIssuer(m)
	var out []Message
	for i, message := range messages {
		if message.Role != "assistant" || message.ReasoningSignature == "" || message.ReasoningBy == "" || message.ReasoningBy == self {
			continue
		}
		if out == nil {
			out = append([]Message(nil), messages...)
		}
		out[i].ReasoningSignature = ""
	}
	if out == nil {
		return messages
	}
	return out
}
