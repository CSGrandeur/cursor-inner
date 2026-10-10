package provider

import (
	"regexp"
	"strings"

	"cursor-inner/internal/config"
)

// Family 是按模型名与接口主机归类的适配档位。
type Family string

const (
	FamilyDeepSeek Family = "deepseek"
	FamilyQwen     Family = "qwen"
	FamilyKimi     Family = "kimi"
	FamilyGLM      Family = "glm"
	FamilyGemini   Family = "gemini"
	FamilyGPT      Family = "gpt"
	FamilyClaude   Family = "claude"
	FamilyMiMo     Family = "mimo"
	FamilyOther    Family = "other"
)

// Profile 描述某一家族在请求与探测上的默认行为。
type Profile struct {
	Family            Family
	ReasoningEcho     bool // OpenAI 兼容请求需回传 reasoning_content
	EmptyReasoning    string
	SupportsTools     bool
	SupportsImages    bool
	SupportsCache     bool
	DefaultContext    int
	DefaultMaxOut     int
	ParallelToolCalls bool // OpenAI 兼容请求里带 parallel_tool_calls=true（有工具时）
	ThinkTagSplit     bool // 流式 content 里的 <think>…</think> 推理标签需剥离到思考通道
}

var profiles = map[Family]Profile{
	FamilyDeepSeek: {Family: FamilyDeepSeek, ReasoningEcho: true, EmptyReasoning: " ", SupportsTools: true, SupportsImages: true, SupportsCache: true, DefaultContext: 128000, DefaultMaxOut: 8192},
	FamilyQwen:     {Family: FamilyQwen, ReasoningEcho: false, SupportsTools: true, SupportsImages: true, SupportsCache: true, DefaultContext: 262144, DefaultMaxOut: 32768, ParallelToolCalls: true, ThinkTagSplit: true},
	FamilyKimi:     {Family: FamilyKimi, ReasoningEcho: true, EmptyReasoning: " ", SupportsTools: true, SupportsImages: true, SupportsCache: true, DefaultContext: 128000, DefaultMaxOut: 8192},
	FamilyGLM:      {Family: FamilyGLM, ReasoningEcho: false, SupportsTools: true, SupportsImages: true, SupportsCache: false, DefaultContext: 128000, DefaultMaxOut: 4096},
	FamilyGemini:   {Family: FamilyGemini, ReasoningEcho: false, SupportsTools: true, SupportsImages: true, SupportsCache: true, DefaultContext: 1000000, DefaultMaxOut: 8192},
	FamilyGPT:      {Family: FamilyGPT, ReasoningEcho: false, SupportsTools: true, SupportsImages: true, SupportsCache: true, DefaultContext: 128000, DefaultMaxOut: 16384},
	FamilyClaude:   {Family: FamilyClaude, ReasoningEcho: false, SupportsTools: true, SupportsImages: true, SupportsCache: true, DefaultContext: 200000, DefaultMaxOut: 8192},
	FamilyMiMo:     {Family: FamilyMiMo, ReasoningEcho: true, EmptyReasoning: " ", SupportsTools: true, SupportsImages: false, SupportsCache: false, DefaultContext: 128000, DefaultMaxOut: 8192},
	FamilyOther:    {Family: FamilyOther, SupportsTools: true, SupportsImages: false, SupportsCache: false, DefaultContext: 128000, DefaultMaxOut: 4096},
}

// DetectFamily 按模型名与 BaseURL 主机判定家族。
func DetectFamily(m config.Model) Family {
	blob := strings.ToLower(m.Model + " " + m.DisplayName)
	host := requestHost(m.BaseURL)
	switch {
	case strings.Contains(blob, "deepseek") || hostMatches(host, "api.deepseek.com"):
		return FamilyDeepSeek
	case strings.Contains(blob, "qwen") || strings.Contains(blob, "qwq") || hostMatches(host, "dashscope.aliyuncs.com") || hostMatches(host, "dashscope-intl.aliyuncs.com"):
		return FamilyQwen
	case strings.Contains(blob, "kimi") || hostMatches(host, "api.kimi.com") || hostMatches(host, "moonshot.ai") || hostMatches(host, "moonshot.cn"):
		return FamilyKimi
	case strings.Contains(blob, "glm") || strings.Contains(blob, "chatglm") || hostMatches(host, "open.bigmodel.cn"):
		return FamilyGLM
	case strings.Contains(blob, "gemini") || hostMatches(host, "generativelanguage.googleapis.com"):
		return FamilyGemini
	case strings.Contains(blob, "claude") || hostMatches(host, "api.anthropic.com") || m.Type == "anthropic":
		return FamilyClaude
	case strings.Contains(blob, "gpt") || strings.Contains(blob, "o1") || strings.Contains(blob, "o3") || strings.Contains(blob, "o4") || hostMatches(host, "api.openai.com"):
		return FamilyGPT
	case strings.Contains(blob, "mimo") || hostMatches(host, "xiaomimimo.com"):
		return FamilyMiMo
	default:
		return FamilyOther
	}
}

// ProfileOf 返回模型所属家族的配置档。
func ProfileOf(m config.Model) Profile {
	p, ok := profiles[DetectFamily(m)]
	if !ok {
		return profiles[FamilyOther]
	}
	return p
}

// qwenEnableThinking 仅对 Qwen3 混合思考系列（排除始终思考的 QwQ）返回 enable_thinking：
// 用户开了推理为 true，否则 false，避免 Qwen3 在非推理调用里默认思考拖慢、串出 reasoning。
// 其它家族返回 nil，请求里不带该字段。
func qwenEnableThinking(m config.Model) *bool {
	if DetectFamily(m) != FamilyQwen {
		return nil
	}
	name := strings.ToLower(m.Model)
	if !strings.Contains(name, "qwen3") || strings.Contains(name, "qwq") {
		return nil
	}
	v := m.Reasoning
	return &v
}

var reQwen3Dot = regexp.MustCompile(`^qwen3\.\d`)

// qwenVariantLimits 按模型名给出 Qwen 各变体的输入上下文与单次输出上限（参考 qwen-code tokenLimits）。
// 不是 Qwen 时返回 0,0。用户已显式设置的窗口/输出优先，不被覆盖。
func qwenVariantLimits(model string) (contextWindow, maxOut int) {
	n := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	if i := strings.Index(n, ":"); i >= 0 { // 去掉 Ollama/OpenRouter 标签，如 :32b、:free
		n = n[:i]
	}
	switch {
	case strings.HasPrefix(n, "qwen3-coder-plus"), strings.HasPrefix(n, "qwen3-coder-flash"):
		return 1000000, 65536
	case strings.HasPrefix(n, "qwen3-coder"):
		return 262144, 65536
	case strings.HasPrefix(n, "qwen3-max"):
		return 262144, 32768
	case reQwen3Dot.MatchString(n): // qwen3.5 等
		return 1000000, 65536
	case n == "qwen-plus-latest", n == "qwen-flash-latest":
		return 1000000, 32768
	case strings.HasPrefix(n, "qwq"):
		return 131072, 32768
	case strings.HasPrefix(n, "qwen"):
		return 262144, 32768
	}
	return 0, 0
}

// qwenThinkingBudget 仅在 Qwen3 混合思考且推理开启、且设了 effort 档时，给 DashScope 的 thinking_budget 一个上限，
// 避免思考 token 失控。effort 未设时返回 nil（不发该字段，由服务端默认）。
func qwenThinkingBudget(m config.Model) *int {
	if qwenEnableThinking(m) == nil || !m.Reasoning {
		return nil
	}
	var b int
	switch strings.ToLower(m.Effort) {
	case "low":
		b = 1024
	case "medium":
		b = 8192
	case "high":
		b = 24576
	default:
		return nil
	}
	return &b
}
