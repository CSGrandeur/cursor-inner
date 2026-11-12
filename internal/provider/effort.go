package provider

import (
	"log/slog"
	"strings"

	"cursor-inner/internal/config"
)

// 通用思考档：未知模型保持与旧目录兼容。id 会原样发给上游。
var genericEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

const genericDefaultEffort = "high"

// EffortPolicy 返回该模型在 Cursor 菜单里展示的思考档和默认档。
// 配置了 effort_levels / default_effort 时优先用用户值（兼容旧 JSON：两项都空则走家族或通用列表）。
// 只有文档明确的家族才收窄列表；不编造其它模型的档位。
func EffortPolicy(m config.Model) (levels []string, defaultEffort string) {
	levels = normalizeEffortLevels(m.EffortLevels)
	defaultEffort = canonicalEffort(m.DefaultEffort)
	famLevels, famDefault, documented := documentedEffortSet(m)
	if len(levels) == 0 {
		if documented {
			levels = append([]string(nil), famLevels...)
		} else {
			levels = append([]string(nil), genericEffortLevels...)
		}
	}
	if defaultEffort == "" || !containsEffort(levels, defaultEffort) {
		switch {
		case documented && containsEffort(levels, famDefault):
			defaultEffort = famDefault
		case containsEffort(levels, genericDefaultEffort):
			defaultEffort = genericDefaultEffort
		case len(levels) > 0:
			defaultEffort = levels[0]
		}
	}
	return levels, defaultEffort
}

// ResolveEffort 把用户/Cursor 选中的档位收成可以发给上游的值。
// 在支持列表里则原样透传；空选择不补发（由上游默认），以免改变未知模型的旧行为。
// 选了该模型不支持的档（例如旧客户端在 Qwen4 上仍带 high/max）时不把该值发出去，
// 改用文档或配置中的默认档，并记一条日志。
func ResolveEffort(m config.Model, selected string) string {
	levels, def := EffortPolicy(m)
	sel := canonicalEffort(selected)
	if sel != "" && containsEffort(levels, sel) {
		return sel
	}
	if sel == "" {
		return ""
	}
	if def != "" && def != sel {
		slog.Info("reasoning_effort_unsupported 客户端选了该模型不支持的思考档，改用默认档",
			"model", m.Model, "requested", sel, "using", def)
		return def
	}
	return ""
}

// requestEffort 正式路径用模型上的选择，探测路径用 req.ReasoningEffort 覆盖。
func requestEffort(req chatRequest, m config.Model) string {
	if s := strings.TrimSpace(req.ReasoningEffort); s != "" {
		return s
	}
	return ResolveEffort(m, m.Effort)
}

// documentedEffortSet 只收录有官方依据的家族档位，避免把单一模型的规则套到其它 provider。
// Qwen4（含 qwen4-max-preview）：阿里/千问文档为 low、medium、xhigh，默认 xhigh。
// Qwen3 / 其它家族未在本函数列出，走通用列表。
func documentedEffortSet(m config.Model) (levels []string, defaultEffort string, ok bool) {
	n := canonicalModelName(m.Model)
	if strings.HasPrefix(n, "qwen4") {
		return []string{"low", "medium", "xhigh"}, "xhigh", true
	}
	return nil, "", false
}

func canonicalModelName(model string) string {
	n := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	if i := strings.Index(n, ":"); i >= 0 {
		n = n[:i]
	}
	return n
}

func canonicalEffort(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func normalizeEffortLevels(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		id := canonicalEffort(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func containsEffort(levels []string, id string) bool {
	for _, item := range levels {
		if item == id {
			return true
		}
	}
	return false
}
