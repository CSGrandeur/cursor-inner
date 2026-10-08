package agent

import (
	"strconv"
	"strings"

	"cursor-inner/internal/cursorpb"
)

// TurnParams 是这一轮从 Cursor 模型选择器带来的参数。
type TurnParams struct {
	Effort        string
	Fast          bool
	ContextTokens int
}

func turnParams(run *cursorpb.AgentRunRequest) TurnParams {
	var params TurnParams
	requested := run.GetRequestedModel()
	applySuffix(&params, requested.GetModelId())
	for _, parameter := range requested.GetParameters() {
		switch parameter.GetId() {
		case "effort", "reasoning":
			if value := strings.TrimSpace(parameter.GetValue()); value != "" && value != "none" {
				params.Effort = value
			}
		case "fast":
			params.Fast = parameter.GetValue() == "true"
		case "context":
			if n := parseTokenCount(parameter.GetValue()); n > 0 {
				params.ContextTokens = n
			}
		}
	}
	if requested.GetMaxMode() && params.Effort == "" {
		params.Effort = "max"
	}
	return params
}

// effectiveWindow 取模型配置和本轮选择里较小的那个。配置是模型上限，选择器不能超过它。
// 两边都没写时用 128k。Cursor 3.23 的菜单不接收低于 200k 的档位，仍会带上 200k；配置里的更小窗口这时仍然生效。
func effectiveWindow(configured, requested int) int {
	switch {
	case configured > 0 && requested > 0:
		if requested < configured {
			return requested
		}
		return configured
	case configured > 0:
		return configured
	case requested > 0:
		return requested
	default:
		return 128000
	}
}

func applySuffix(params *TurnParams, modelID string) {
	start := strings.IndexByte(modelID, '[')
	end := strings.LastIndexByte(modelID, ']')
	if start < 0 || end <= start {
		return
	}
	for _, part := range strings.Split(modelID[start+1:end], ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "reasoning", "effort":
			if value != "" && value != "none" {
				params.Effort = value
			}
		case "fast":
			params.Fast = value == "true"
		case "context":
			if n := parseTokenCount(value); n > 0 {
				params.ContextTokens = n
			}
		}
	}
}

func parseTokenCount(value string) int {
	value = strings.TrimSpace(strings.ToLower(value))
	mult := 1
	switch {
	case strings.HasSuffix(value, "k"):
		mult = 1000
		value = strings.TrimSuffix(value, "k")
	case strings.HasSuffix(value, "m"):
		mult = 1000000
		value = strings.TrimSuffix(value, "m")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0
	}
	return n * mult
}
