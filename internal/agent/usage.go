package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf16"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// Cursor Context Usage 面板的分类 id/label，与 cursor-byok 及官方 UI 对齐。
var tokenCategories = [8]struct{ id, label string }{
	{"system_prompt", "System prompt"},
	{"tools", "Tool definitions"},
	{"rules", "Rules"},
	{"skills", "Skills"},
	{"mcp", "MCP & dynamic tools"},
	{"subagents", "Subagent definitions"},
	{"summarized_conversation", "Summarized conversation"},
	{"conversation", "Conversation"},
}

const (
	catSystem = iota
	catTools
	catRules
	catSkills
	catMCP
	catSubagents
	catSummary
	catConversation
)

type span struct{ start, end, cat int }

type tokenMeasure struct {
	characters uint64
	units      uint64
}

func (m *tokenMeasure) add(text string) {
	if text == "" {
		return
	}
	encoded := utf16.Encode([]rune(text))
	m.characters += uint64(len(encoded))
	for _, r := range text {
		width := uint64(1)
		if r > 0xFFFF {
			width = 2
		}
		if r <= 0x7F {
			m.units += width * 273
		} else {
			m.units += width * 550
		}
	}
}

func (m tokenMeasure) estimatedTokens() uint64 {
	if m.units == 0 {
		return 0
	}
	return (m.units + 999) / 1000
}

// tokenBreakdown 按官方 Context Usage 分类估算 token，写入 ConversationTokenDetails.breakdown。
// 计数是近似值（对齐 cursor-byok 的 UTF-16 加权），再按 used 总额拟合，使色条能出同一套 UI。
func tokenBreakdown(used, max uint32, baseline *cursorpb.PromptTokenBreakdownSnapshot, system string, tools []provider.Tool, mcpNames map[string]struct{}, messages []provider.Message) *cursorpb.PromptTokenBreakdownSnapshot {
	var measures [8]tokenMeasure
	measureTagged(system, &measures, true)
	for _, tool := range tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			continue
		}
		if _, ok := mcpNames[tool.Name]; ok {
			measures[catMCP].add(string(encoded))
		} else {
			measures[catTools].add(string(encoded))
		}
	}
	for _, message := range messages {
		measureMessage(message, &measures)
	}

	var estimates [8]uint64
	for i := 0; i < catConversation; i++ {
		estimates[i] = measures[i].estimatedTokens()
	}
	if measures[catSummary].characters != 0 {
		estimates[catSummary] = measures[catSummary].estimatedTokens()
	} else if baseline != nil {
		for _, category := range baseline.GetCategories() {
			if category.GetId() == tokenCategories[catSummary].id {
				if n := category.GetCharacterCount(); n > 0 {
					measures[catSummary].characters = uint64(n)
				}
				estimates[catSummary] = uint64(category.GetEstimatedTokens())
				break
			}
		}
	}
	total := uint64(used)
	fitSpecialEstimates(&estimates, total)
	special := uint64(0)
	for i := 0; i < catConversation; i++ {
		special += estimates[i]
	}
	if total > special {
		estimates[catConversation] = total - special
	} else {
		estimates[catConversation] = 0
	}

	categories := make([]*cursorpb.PromptTokenBreakdownCategory, 0, 8)
	for i, meta := range tokenCategories {
		cat := &cursorpb.PromptTokenBreakdownCategory{
			Id:              meta.id,
			Label:           meta.label,
			EstimatedTokens: uint32(min64(estimates[i], uint64(^uint32(0)))),
		}
		if measures[i].characters != 0 {
			n := uint32(min64(measures[i].characters, uint64(^uint32(0))))
			cat.CharacterCount = &n
		}
		categories = append(categories, cat)
	}
	return &cursorpb.PromptTokenBreakdownSnapshot{
		TotalUsedTokens: used,
		MaxTokens:       max,
		Categories:      categories,
	}
}

func measureMessage(message provider.Message, measures *[8]tokenMeasure) {
	if message.Role == "assistant" {
		measures[catConversation].add(message.Content)
		measures[catConversation].add(message.Reasoning)
		if len(message.ToolCalls) > 0 {
			if raw, err := json.Marshal(message.ToolCalls); err == nil {
				measures[catConversation].add(string(raw))
			}
		}
		return
	}
	if message.Role == "tool" {
		measures[catConversation].add(message.Content)
		if raw, err := json.Marshal(message.Images); err == nil && len(message.Images) > 0 {
			measures[catConversation].add(string(raw))
		}
		return
	}
	measureTagged(message.Content, measures, false)
}

// measureTagged 把带官方标签的段落归入对应分类；其余进 system（isSystem）或 conversation。
func measureTagged(text string, measures *[8]tokenMeasure, isSystem bool) {
	if text == "" {
		return
	}
	fallback := catConversation
	if isSystem {
		fallback = catSystem
	}
	var ranges []span
	collectTagRanges(text, "rules", catRules, &ranges)
	collectTagRanges(text, "agent_requestable_rules", catRules, &ranges)
	collectTagRanges(text, "rule", catRules, &ranges)
	collectTagRanges(text, "agent_skills", catSkills, &ranges)
	collectTagRanges(text, "skill", catSkills, &ranges)
	collectTagRanges(text, "subagents", catSubagents, &ranges)
	collectTagRanges(text, "mcp_meta_tools", catMCP, &ranges)
	collectTagRanges(text, "mcp_meta_tool_server", catMCP, &ranges)
	collectTagRanges(text, "conversation_summary", catSummary, &ranges)
	collectTagRanges(text, "summary", catSummary, &ranges)
	sortSpans(ranges)

	cursor := 0
	for _, r := range ranges {
		if r.start < cursor {
			continue
		}
		measures[fallback].add(text[cursor:r.start])
		measures[r.cat].add(text[r.start:r.end])
		cursor = r.end
	}
	measures[fallback].add(text[cursor:])
}

func collectTagRanges(text, tag string, category int, out *[]span) {
	opening := "<" + tag
	closing := "</" + tag + ">"
	cursor := 0
	for cursor < len(text) {
		rel := strings.Index(text[cursor:], opening)
		if rel < 0 {
			return
		}
		start := cursor + rel
		if !tagOpenBoundary(text, start, tag) {
			cursor = start + len(opening)
			continue
		}
		gt := strings.IndexByte(text[start:], '>')
		if gt < 0 {
			return
		}
		openEnd := start + gt + 1
		relEnd := strings.Index(text[openEnd:], closing)
		if relEnd < 0 {
			return
		}
		end := openEnd + relEnd + len(closing)
		*out = append(*out, span{start, end, category})
		cursor = end
	}
}

func tagOpenBoundary(text string, start int, tag string) bool {
	pos := start + 1 + len(tag)
	if pos > len(text) {
		return false
	}
	if pos == len(text) {
		return true
	}
	switch text[pos] {
	case '>', ' ', '\t', '\n', '\r', '/':
		return true
	default:
		return false
	}
}

func sortSpans(ranges []span) {
	for i := 1; i < len(ranges); i++ {
		j := i
		for j > 0 && ranges[j].start < ranges[j-1].start {
			ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
			j--
		}
	}
}

func fitSpecialEstimates(estimates *[8]uint64, total uint64) {
	special := uint64(0)
	for i := 0; i < catConversation; i++ {
		special += estimates[i]
	}
	if special <= total || special == 0 {
		return
	}
	original := *estimates
	assigned := uint64(0)
	for i := 0; i < catConversation; i++ {
		estimates[i] = original[i] * total / special
		assigned += estimates[i]
	}
	remainder := total - assigned
	order := make([]int, catConversation)
	for i := range order {
		order[i] = i
	}
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			remI := original[order[i]] * total % special
			remJ := original[order[j]] * total % special
			if remJ > remI {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	for _, i := range order {
		if remainder == 0 {
			break
		}
		estimates[i]++
		remainder--
	}
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func mcpNameSet(defs []*cursorpb.McpToolDefinition) map[string]struct{} {
	if len(defs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		if def == nil {
			continue
		}
		if name := strings.TrimSpace(def.GetName()); name != "" {
			out[name] = struct{}{}
		}
		// Merge 重名时可能加服务器前缀；Match 两边都认，这里把原始名和带前缀的都记上。
		if name := strings.TrimSpace(def.GetToolName()); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

func prevBreakdown(prev *cursorpb.ConversationStateStructure) *cursorpb.PromptTokenBreakdownSnapshot {
	if prev == nil {
		return nil
	}
	return prev.GetTokenDetails().GetBreakdown()
}
