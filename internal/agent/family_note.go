package agent

import (
	"strings"

	"cursor-inner/internal/config"
	"cursor-inner/internal/provider"
)

// familyNote 按模型家族补几条提示，针对各家常见的失误（参考 Hermes / DeepSeek / Qwen / Kimi
// 官方 agent 提示与 Aider、Roo Code 的模型适配经验）。Claude 本身遵循得好，不加。
func familyNote(m config.Model) string {
	family := provider.DetectFamily(m)
	var lines []string
	switch family {
	case provider.FamilyClaude:
		return ""
	case provider.FamilyGPT:
		lines = append(lines,
			"- To edit files use StrReplace or Write. apply_patch is also accepted, one file per patch.",
			"- Keep going until the task is done; do not stop to ask for confirmation of routine steps.")
	case provider.FamilyGemini:
		lines = append(lines,
			"- Pass tool arguments as plain JSON values; never wrap them in markdown fences.",
			"- Read a file before editing it, and edit with StrReplace instead of rewriting whole files.")
	default:
		lines = append(lines,
			"- Call tools only through the function-calling interface. Never write tool calls, XML tags or JSON tool payloads in the reply text.",
			"- Copy old_string for StrReplace exactly from the latest Read output, without the line-number prefix, with enough surrounding lines to be unique.",
			"- After a tool error, read the message, re-read the file if needed, and change the approach instead of repeating the same call.")
		switch family {
		case provider.FamilyDeepSeek:
			lines = append(lines, "- Keep reasoning brief; do not restate the whole plan before every tool call.")
		case provider.FamilyQwen:
			lines = append(lines,
				"- Emit tool calls only through the native function-calling interface; never print <tool_call>, <function=...> or <invoke name=...> tags, and never wrap tool arguments in markdown code fences.",
				"- Inside tool arguments write code literally; do not XML-escape characters such as <, > or &.",
				"- Prefer one focused tool call per turn and read a file before editing it.")
		case provider.FamilyKimi:
			lines = append(lines, "- Do not emit <|tool_call_begin|> style markup in the reply.")
		case provider.FamilyGLM:
			lines = append(lines, "- Reply in the user's language and keep answers concise.")
		}
	}
	return "\n<model_notes>\n" + strings.Join(lines, "\n") + "\n</model_notes>\n"
}
