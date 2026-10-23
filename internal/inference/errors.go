package inference

import (
	"errors"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"
)

// Cursor 3.26.13 cursor-agent-host 如何处理 invocation_end.error（见 docs/local-loop-protocol.md「错误」）：
// RunInferenceInvocationError{code, message, details[{type, value}]} 被还原成 ConnectError，
// details 里 type 为 "aiserver.v1.ErrorDetails" 的一条决定分类：
//   - ErrorDetails.error = CUSTOM_MESSAGE(29) 且 details.is_retryable != true → NonRetriableError，
//     界面显示 details.title / details.detail，回合停止；
//   - 没有 ErrorDetails 时一律当 RetriableError，界面显示 "Reconnecting..." 并重开 RunInference 重试。
//
// 不用 BAD_API_KEY / NOT_LOGGED_IN 之类：那些会弹 Cursor 自己的“配置/登录/升级”动作，不适合自定义模型。
const (
	errorDetailsType   = "aiserver.v1.ErrorDetails"
	errCustomMessage   = 29
	codeFailedPrecond  = 9
	codeUnavailable    = 14
	maxTransientRounds = 2 // 同一对话连续几次临时失败交给 Cursor 重试；再失败就停下说明原因
)

// failure 是一次模型调用失败给 Cursor 的答复。
type failure struct {
	code      int
	message   string
	title     string
	detail    string
	retryable bool // true：不带 ErrorDetails，让 Cursor 自己重试
}

func quotaExhausted(api *provider.APIError) bool {
	if api.Status == 402 {
		return true
	}
	t := strings.ToLower(api.Text + " " + api.Detail)
	for _, k := range []string{"insufficient_quota", "insufficient balance", "insufficient_balance", "quota exceeded", "exceeded your current quota", "arrearage", "余额不足", "欠费", "额度"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// classify 把模型错误分成“停下并说明”和“让 Cursor 重试”。transientSoFar 是本对话此前连续临时失败的次数。
func classify(err error, model string, transientSoFar int) failure {
	why := provider.Explain(err)
	var api *provider.APIError
	isAPI := errors.As(err, &api)
	stop := func(title, hint i18n.Text) failure {
		detail := hint.String()
		if why != "" {
			detail += "\n\n" + i18n.Tf("模型返回：%s", "Model said: %s", why).String()
		}
		return failure{code: codeFailedPrecond, message: why, title: title.String(), detail: detail}
	}
	switch {
	case isAPI && quotaExhausted(api):
		// 上游常把欠费报成 401/403/429，Explain 会说“密钥被拒绝/限流”，这里只给原话。
		if api.Detail != "" {
			why = api.Detail
		}
		return stop(i18n.Tf("自定义模型 %s 额度用完了", "Custom model %s is out of quota", model),
			i18n.T("这个模型的账户余额或额度不足。请到模型服务商处充值或换一个模型，然后重试。", "The account for this model has no balance or quota left. Top it up with the provider or pick another model, then retry."))
	case isAPI && api.Kind == provider.KindAuth:
		return stop(i18n.Tf("自定义模型 %s 的密钥被拒绝", "Custom model %s rejected the API key", model),
			i18n.T("请在 cursor-inner 设置页检查这个模型的 API Key 和接口地址，然后重试。", "Check this model's API key and base URL in cursor-inner settings, then retry."))
	case isAPI && api.Kind == provider.KindContextOverflow:
		return stop(i18n.Tf("对话太长，%s 放不下", "This chat is too long for %s", model),
			i18n.T("请开一个新对话，或换一个上下文更大的模型。", "Start a new chat or switch to a model with a larger context window."))
	case isAPI && api.Kind == provider.KindBadRequest:
		return stop(i18n.Tf("自定义模型 %s 拒绝了这次请求", "Custom model %s rejected the request", model),
			i18n.T("模型服务商认为请求不合法，重试也会一样。请检查 cursor-inner 里这个模型的设置，或换一个模型。", "The provider rejected this request as invalid, so retrying will not help. Check this model's settings in cursor-inner or pick another model."))
	case provider.Retryable(err) || (!isAPI && isTransport(err)):
		if transientSoFar < maxTransientRounds {
			return failure{code: codeUnavailable, message: why, retryable: true}
		}
		return stop(i18n.Tf("暂时连不上自定义模型 %s", "Can't reach custom model %s right now", model),
			i18n.T("已经重试多次仍然失败。请检查网络、代理和模型服务状态，稍后点重试。", "Several retries failed. Check the network, proxy and the provider's status, then retry later."))
	default:
		return stop(i18n.Tf("自定义模型 %s 调用失败", "Custom model %s failed", model),
			i18n.T("请检查 cursor-inner 里这个模型的设置后重试。", "Check this model's settings in cursor-inner, then retry."))
	}
}

func isTransport(err error) bool {
	t := strings.ToLower(err.Error())
	for _, k := range []string{"connection refused", "connection reset", "no such host", "timeout", "eof", "broken pipe"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// endFor 编码 invocation_end：可重试时只带 code/message；要停下时附 ErrorDetails{CUSTOM_MESSAGE, {title, detail, is_retryable=false}}。
func endFor(id string, f failure) []byte {
	b := appendStr(nil, 1, id)
	e := appendInt(nil, 1, f.code)
	e = appendStr(e, 2, f.message)
	if !f.retryable {
		custom := appendStr(nil, 1, f.title)
		custom = appendStr(custom, 2, f.detail)
		custom = appendBoolAlways(custom, 4, false)
		ed := appendInt(nil, 1, errCustomMessage)
		ed = appendBytes(ed, 2, custom)
		d := appendStr(nil, 1, errorDetailsType)
		d = appendBytes(d, 2, ed)
		e = appendBytes(e, 3, d)
	}
	b = appendBytes(b, 2, e)
	return msg(4, b)
}

func appendBytes(b []byte, num int, v []byte) []byte {
	b = protowire.AppendTag(b, protowire.Number(num), protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

// appendBoolAlways 连 false 也写出（proto3 optional 字段要有“存在”）。
func appendBoolAlways(b []byte, num int, v bool) []byte {
	b = protowire.AppendTag(b, protowire.Number(num), protowire.VarintType)
	return protowire.AppendVarint(b, protowire.EncodeBool(v))
}
