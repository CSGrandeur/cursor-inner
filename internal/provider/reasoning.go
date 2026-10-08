package provider

import (
	"net/url"
	"strings"

	"cursor-inner/internal/config"
)

// needsReasoningEcho 判断这个接口是否要求回传 reasoning_content。
// DeepSeek、Kimi、MiMo 在思考模式下缺这个字段会返回 400；其他严格接口则必须删掉该字段。
func needsReasoningEcho(m config.Model) bool {
	blob := strings.ToLower(m.Model + " " + m.DisplayName)
	host := requestHost(m.BaseURL)
	if strings.Contains(blob, "deepseek") || hostMatches(host, "api.deepseek.com") {
		return true
	}
	if strings.Contains(blob, "kimi") || hostMatches(host, "api.kimi.com") || hostMatches(host, "moonshot.ai") || hostMatches(host, "moonshot.cn") {
		return true
	}
	if strings.Contains(blob, "mimo") || hostMatches(host, "xiaomimimo.com") {
		return true
	}
	return false
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
// 需要回传时，空内容用单个空格：DeepSeek V4 拒绝空字符串。不需要回传时返回 nil，字段不会出现在 JSON 里。
func reasoningField(m config.Model, message Message) *string {
	if message.Role != "assistant" || !needsReasoningEcho(m) {
		return nil
	}
	value := message.Reasoning
	if value == "" {
		value = " "
	}
	return &value
}
