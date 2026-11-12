package provider

import (
	"context"
	"net/http/httptrace"
	"strings"
)

// probeOpts 只给设置页能力探测用，正式 Chat/Responses 路径不会带这些字段。
// 依据千问/阿里 Responses 文档：扁平 tools、reasoning.effort=low 加快首包，
// 不发送 thinking_budget（Responses 不支持），也不发送官方未列出的 include。
type probeOpts struct {
	ToolChoice      string
	ReasoningEffort string
	OmitInclude     bool
	FlatTools       bool
}

type probeOptsKey struct{}
type headerNotifyKey struct{}

func withProbeOpts(ctx context.Context, o probeOpts) context.Context {
	return context.WithValue(ctx, probeOptsKey{}, o)
}

func probeOptsFrom(ctx context.Context) probeOpts {
	o, _ := ctx.Value(probeOptsKey{}).(probeOpts)
	return o
}

func withHeaderNotify(ctx context.Context, fn func()) context.Context {
	if fn == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, headerNotifyKey{}, fn)
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			fn()
		},
	})
}

// probeReasoningEffort 按官方文档给探测请求选择更快的思考档，不改用户配置，也不套用 qwen3 旧规则。
// qwen4-max-preview：混合思考默认开启，可选 low / medium / xhigh（默认 xhigh）；降低档位可加快响应、减少推理 token。
// Qwen4.0 / Qwen3.8 Responses 文档同样列出 low。不发送 none，以免踩到文档不一致（最新模型页未列出 none）。
// 不向 Responses 发送 thinking_budget。
func probeReasoningEffort(model string) string {
	n := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	if i := strings.Index(n, ":"); i >= 0 {
		n = n[:i]
	}
	if strings.HasPrefix(n, "qwen4") || strings.HasPrefix(n, "qwen3.8") {
		return "low"
	}
	return ""
}
