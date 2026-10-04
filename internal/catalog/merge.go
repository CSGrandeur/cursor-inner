package catalog

import (
	"fmt"
	"strings"

	"cursor-inner/internal/protox"
)

type Entry struct {
	ID          string
	DisplayName string
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
	contexts = [][2]string{{"200k", "200K"}, {"356k", "356K"}, {"500k", "500K"}, {"800k", "800K"}, {"1m", "1M"}}
	efforts  = [][2]string{{"low", "Low"}, {"medium", "Medium"}, {"high", "High"}, {"xhigh", "Extra High"}, {"max", "Max"}}
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
	msg = protox.AppendString(msg, 17, name)
	msg = protox.AppendString(msg, 18, model.ID)
	msg = protox.AppendBool(msg, 19, true)
	msg = protox.AppendBytes(msg, 20, tooltip)
	msg = protox.AppendBool(msg, 22, true)
	msg = protox.AppendString(msg, 24, name)
	msg = protox.AppendBool(msg, 25, true)
	msg = append(msg, parameterDefinitions()...)
	msg = append(msg, variants(model.ID, name, tooltip)...)
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

func parameterDefinitions() []byte {
	var out []byte
	out = protox.AppendBytes(out, 29, enumParam("context", "Context", "Context size used to trigger conversation compaction.", contexts, false))
	out = protox.AppendBytes(out, 29, enumParam("reasoning", "Effort", "Effort the model uses to generate its response.", efforts, true))
	out = protox.AppendBytes(out, 29, fastParam())
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

func variants(id, name string, tooltip []byte) []byte {
	var out []byte
	for _, context := range contexts {
		for _, effort := range efforts {
			for _, fast := range []bool{false, true} {
				out = protox.AppendBytes(out, 30, variant(id, name, tooltip, context, effort, fast))
			}
		}
	}
	return out
}

func variant(id, name string, tooltip []byte, context, effort [2]string, fast bool) []byte {
	parts := make([]string, 0, 3)
	if context[0] != "200k" {
		parts = append(parts, context[1])
	}
	parts = append(parts, effort[1])
	if fast {
		parts = append(parts, "Fast")
	}
	shown := name
	if len(parts) > 0 {
		shown = fmt.Sprintf("%s <span style=\"color: var(--cursor-text-tertiary);\">%s</span>", name, strings.Join(parts, " "))
	}
	def := context[0] == "200k" && effort[0] == "high" && !fast
	slug := id + "-" + context[0] + "-" + effort[0]
	if fast {
		slug += "-fast"
	}
	params := protox.AppendBytes(nil, 1, kv("context", context[0]))
	params = protox.AppendBytes(params, 1, kv("reasoning", effort[0]))
	params = protox.AppendBytes(params, 1, kv("fast", fmt.Sprintf("%t", fast)))
	msg := params
	msg = protox.AppendString(msg, 2, shown)
	if def {
		msg = protox.AppendBool(msg, 4, true)
		msg = protox.AppendBool(msg, 5, true)
	}
	msg = protox.AppendBytes(msg, 6, tooltip)
	msg = protox.AppendString(msg, 8, shown)
	msg = protox.AppendString(msg, 9, fmt.Sprintf("%s[context=%s,reasoning=%s,fast=%t]", id, context[0], effort[0], fast))
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
