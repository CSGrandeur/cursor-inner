package provider

import (
	"net/url"
	"strings"

	"cursor-inner/internal/config"
)

// needsReasoningEcho 判断这个接口是否要求回传 reasoning_content。
// 规则收进家族表；DeepSeek / Kimi / MiMo 在思考模式下缺字段会 400。
func needsReasoningEcho(m config.Model) bool {
	return ProfileOf(m).ReasoningEcho
}

func requestHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func hostMatches(host, want string) bool {
	return host == want || strings.HasSuffix(host, "."+want)
}

// reasoningField 决定 OpenAI 请求里助手消息是否带 reasoning_content。
// 需要回传时，空内容用家族表的 EmptyReasoning（DeepSeek 系为单个空格）。
func reasoningField(m config.Model, message Message) *string {
	if message.Role != "assistant" {
		return nil
	}
	p := ProfileOf(m)
	if !p.ReasoningEcho {
		return nil
	}
	value := message.Reasoning
	if value == "" {
		value = p.EmptyReasoning
		if value == "" {
			value = " "
		}
	}
	return &value
}
