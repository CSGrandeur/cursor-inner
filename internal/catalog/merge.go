package catalog

import (
	"fmt"
	"strconv"
	"strings"

	"cursor-inner/internal/protox"
)

type Entry struct {
	ID            string
	DisplayName   string
	Reasoning     bool
	Fast          bool
	ContextWindow int
}

func Available(models []Entry) []byte {
	var out []byte
	for _, model := range models {
		out = protox.AppendString(out, 1, model.ID)
		out = protox.AppendBytes(out, 2, availableModel(model))
	}
	return out
}

func Usable(models []Entry) []byte {
	var out []byte
	for _, model := range models {
		out = protox.AppendBytes(out, 1, modelDetails(model))
	}
	return out
}

var (
	contexts     = [][2]string{{"200k", "200K"}, {"356k", "356K"}, {"500k", "500K"}, {"800k", "800K"}, {"1m", "1M"}}
	contextSizes = map[string]int{"200k": 200000, "356k": 356000, "500k": 500000, "800k": 800000, "1m": 1000000}
	efforts      = [][2]string{{"low", "Low"}, {"medium", "Medium"}, {"high", "High"}, {"xhigh", "Extra High"}, {"max", "Max"}}
)

func availableModel(model Entry) []byte {
	name := displayName(model)
	tooltip := protox.AppendString(nil, 7, name)
	var msg []byte
	msg = protox.AppendString(msg, 1, model.ID)
	msg = protox.AppendBool(msg, 2, true)
	msg = protox.AppendBool(msg, 5, true)
	msg = protox.AppendVarint(msg, 6, 0)
	msg = protox.AppendBytes(msg, 8, tooltip)
	msg = protox.AppendBool(msg, 9, true)
	msg = protox.AppendBool(msg, 10, true)
	msg = protox.AppendBool(msg, 14, true)
	choices, defaultContext := contextChoices(model.ContextWindow)
	// 15/16 是 Cursor 用来算上下文占用的“默认窗口”和“最大窗口”（官方目录里 Grok 4.7 =
	// 256k/500k，Claude = 300k/1m）。不写这两个字段时客户端按 256K 兜底：选择器里选了 1M，
	// 右上角的上下文预览仍然显示 /256K。
	if n := contextTokens(defaultContext); n > 0 {
		msg = protox.AppendVarint(msg, 15, uint64(n))
	}
	maxWindow := model.ContextWindow
	if maxWindow <= 0 {
		maxWindow = largestContext(choices)
	}
	if maxWindow > 0 {
		msg = protox.AppendVarint(msg, 16, uint64(maxWindow))
	}
	msg = protox.AppendString(msg, 17, name)
	msg = protox.AppendString(msg, 18, model.ID)
	msg = protox.AppendBool(msg, 19, true)
	msg = protox.AppendBytes(msg, 20, tooltip)
	msg = protox.AppendBool(msg, 22, true)
	msg = protox.AppendString(msg, 24, name)
	msg = protox.AppendBool(msg, 25, true)
	msg = append(msg, parameterDefinitions(model.Reasoning, model.Fast, choices)...)
	msg = append(msg, variants(model.ID, name, tooltip, model.Reasoning, model.Fast, choices, defaultContext)...)
	msg = protox.AppendVarint(msg, 38, 1)
	msg = protox.AppendString(msg, 41, "cursor-inner")
	vendor := protox.AppendVarint(nil, 1, 6)
	vendor = protox.AppendString(vendor, 2, "cursor-inner")
	msg = protox.AppendBytes(msg, 42, vendor)
	badge := protox.AppendString(nil, 1, name)
	badge = protox.AppendVarint(badge, 2, 1)
	msg = protox.AppendBytes(msg, 48, badge)
	return msg
}

// contextChoices 只提供模型装得下的档位。超过模型上限的档位选中后会被 effectiveWindow 截断，
// 菜单里却照旧显示成可用，用户只会看到“选了 1M，实际还是 128K”。模型设置的上限本身
// 不在固定档位表里时（例如 128K），插到最前并作为默认档。
func contextChoices(window int) (list [][2]string, defaultID string) {
	list = append([][2]string{}, contexts...)
	if window <= 0 {
		// 不知道模型上限时保留全部档位，由 effectiveWindow 兜底。
		return list, "200k"
	}
	kept := list[:0]
	for _, item := range list {
		if size, ok := contextSizes[item[0]]; ok && size > window {
			continue
		}
		kept = append(kept, item)
	}
	list = kept
	id, label := formatContext(window)
	if id == "" {
		return list, "200k"
	}
	for i, item := range list {
		if item[0] == id {
			return list, list[i][0]
		}
	}
	return append([][2]string{{id, label}}, list...), id
}

// contextTokens 把档位 id 换算成 token 数：固定档位查表，模型自带的档位（128k、300000）按数字解析。
func contextTokens(id string) int {
	if n, ok := contextSizes[id]; ok {
		return n
	}
	value := strings.TrimSpace(strings.ToLower(id))
	mult := 1
	switch {
	case strings.HasSuffix(value, "m"):
		mult, value = 1000000, strings.TrimSuffix(value, "m")
	case strings.HasSuffix(value, "k"):
		mult, value = 1000, strings.TrimSuffix(value, "k")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0
	}
	return n * mult
}

// largestContext 返回档位表里最大的 token 数，用于模型没设上限时的“最大窗口”。
func largestContext(list [][2]string) int {
	max := 0
	for _, item := range list {
		if n := contextTokens(item[0]); n > max {
			max = n
		}
	}
	return max
}

func formatContext(window int) (id, label string) {
	if window <= 0 {
		return "", ""
	}
	switch {
	case window%1000000 == 0:
		n := window / 1000000
		return strconv.Itoa(n) + "m", strconv.Itoa(n) + "M"
	case window%1000 == 0:
		n := window / 1000
		return strconv.Itoa(n) + "k", strconv.Itoa(n) + "K"
	default:
		text := strconv.Itoa(window)
		return text, text
	}
}

func parameterDefinitions(reasoning, fast bool, choices [][2]string) []byte {
	var out []byte
	out = protox.AppendBytes(out, 29, enumParam("context", "Context", "Context size used to trigger conversation compaction.", choices, false))
	if reasoning {
		out = protox.AppendBytes(out, 29, enumParam("reasoning", "Effort", "Effort the model uses to generate its response.", efforts, true))
	}
	if fast {
		out = protox.AppendBytes(out, 29, fastParam())
	}
	return out
}

func enumParam(id, name, tip string, values [][2]string, hotkey bool) []byte {
	var enum []byte
	for _, value := range values {
		item := protox.AppendString(nil, 1, value[0])
		item = protox.AppendString(item, 2, value[1])
		enum = protox.AppendBytes(enum, 1, item)
	}
	ptype := protox.AppendBytes(nil, 2, enum)
	def := protox.AppendString(nil, 1, id)
	def = protox.AppendString(def, 2, name)
	def = protox.AppendString(def, 3, tip)
	def = protox.AppendBytes(def, 4, ptype)
	if hotkey {
		def = protox.AppendBool(def, 5, true)
	}
	return def
}

func fastParam() []byte {
	off := protox.AppendString(nil, 1, "false")
	on := protox.AppendString(nil, 1, "true")
	on = protox.AppendString(on, 2, "Fast")
	on = protox.AppendBool(on, 3, true)
	boolean := protox.AppendBytes(nil, 1, off)
	boolean = protox.AppendBytes(boolean, 1, on)
	ptype := protox.AppendBytes(nil, 1, boolean)
	def := protox.AppendString(nil, 1, "fast")
	def = protox.AppendString(def, 2, "Fast")
	def = protox.AppendString(def, 3, "Significantly faster but consumes more usage")
	def = protox.AppendBytes(def, 4, ptype)
	return def
}

func variants(id, name string, tooltip []byte, reasoning, fastSupport bool, contextList [][2]string, defaultContext string) []byte {
	choices := [][2]string{{"", ""}}
	if reasoning {
		choices = efforts
	}
	speeds := []bool{false}
	if fastSupport {
		speeds = []bool{false, true}
	}
	var out []byte
	for _, context := range contextList {
		for _, effort := range choices {
			for _, fast := range speeds {
				out = protox.AppendBytes(out, 30, variant(id, name, tooltip, context, effort, fast, reasoning, defaultContext))
			}
		}
	}
	return out
}

func variant(id, name string, tooltip []byte, context, effort [2]string, fast, reasoning bool, defaultContext string) []byte {
	parts := make([]string, 0, 3)
	if context[0] != defaultContext {
		parts = append(parts, context[1])
	}
	if reasoning && effort[1] != "" {
		parts = append(parts, effort[1])
	}
	if fast {
		parts = append(parts, "Fast")
	}
	shown := name
	if len(parts) > 0 {
		shown = fmt.Sprintf("%s <span style=\"color: var(--cursor-text-tertiary);\">%s</span>", name, strings.Join(parts, " "))
	}
	def := context[0] == defaultContext && !fast && (!reasoning || effort[0] == "high")
	slug := id + "-" + context[0]
	if reasoning {
		slug += "-" + effort[0]
	}
	if fast {
		slug += "-fast"
	}
	params := protox.AppendBytes(nil, 1, kv("context", context[0]))
	if reasoning {
		params = protox.AppendBytes(params, 1, kv("reasoning", effort[0]))
	}
	params = protox.AppendBytes(params, 1, kv("fast", fmt.Sprintf("%t", fast)))
	msg := params
	msg = protox.AppendString(msg, 2, shown)
	if def {
		msg = protox.AppendBool(msg, 4, true)
		msg = protox.AppendBool(msg, 5, true)
	}
	msg = protox.AppendBytes(msg, 6, tooltip)
	msg = protox.AppendString(msg, 8, shown)
	suffix := fmt.Sprintf("%s[context=%s,fast=%t]", id, context[0], fast)
	if reasoning {
		suffix = fmt.Sprintf("%s[context=%s,reasoning=%s,fast=%t]", id, context[0], effort[0], fast)
	}
	msg = protox.AppendString(msg, 9, suffix)
	msg = protox.AppendString(msg, 11, slug)
	return msg
}

func kv(id, value string) []byte {
	msg := protox.AppendString(nil, 1, id)
	return protox.AppendString(msg, 2, value)
}

func modelDetails(model Entry) []byte {
	name := displayName(model)
	var msg []byte
	msg = protox.AppendString(msg, 1, model.ID)
	msg = protox.AppendBytes(msg, 2, nil)
	msg = protox.AppendString(msg, 3, model.ID)
	msg = protox.AppendString(msg, 4, name)
	msg = protox.AppendString(msg, 5, name)
	cred := protox.AppendString(nil, 1, "cursor-inner-local")
	return protox.AppendBytes(msg, 8, cred)
}

func displayName(model Entry) string {
	if model.DisplayName != "" {
		return model.DisplayName
	}
	return model.ID
}
