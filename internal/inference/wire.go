// Package inference 在本机回答 Cursor 3.26 “本地循环”（agent_host_local_loop）的
// aiserver.v1.InferenceService/RunInference。客户端自己跑 agent 循环、拼提示词、执行工具，
// 只向服务端要“这一轮模型输出”：InvokeModel 带完整的 InferenceStreamRequest（消息 + 工具），
// 服务端用 InferenceStreamResponse 流回文本、思考、工具调用和用量。
//
// 字段号取自 Cursor 3.26.13 extensions/cursor-agent-host/dist/main.js 的消息描述（见 docs/local-loop-protocol.md）。
package inference

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"cursor-inner/internal/provider"
)

// RunInferenceClientMessage oneof
const (
	ClientRunRequest       = 1
	ClientInvokeModel      = 2
	ClientCancelInvocation = 3
	ClientFinishRun        = 4
	ClientHeartbeat        = 5
	ClientAgentEvent       = 6
	ClientRunRequestLog    = 7
	ClientResolveSubagents = 9
	ClientFirstCallTimings = 10
)

// InferenceMessageRole
const (
	roleUser      = 1
	roleAssistant = 2
	roleTool      = 3
	roleSystem    = 4
)

type RunRequest struct {
	ConversationID string
	ModelID        string
	MaxMode        bool
}

type Invoke struct {
	ID       string
	System   string
	Messages []provider.Message
	Tools    []provider.Tool
	// Unsupported 非空时这次调用用了 cursor-inner 不支持的形式（压缩/增量请求）。
	Unsupported string
}

type ClientMessage struct {
	Kind     int
	Run      *RunRequest
	Invoke   *Invoke
	CancelID string
	Aborted  bool
}

var errMalformed = errors.New("malformed RunInferenceClientMessage")

type field struct {
	num int
	typ protowire.Type
	v   uint64
	b   []byte
}

func fields(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, errMalformed
		}
		b = b[n:]
		f := field{num: int(num), typ: typ}
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return nil, errMalformed
			}
			f.v, b = v, b[m:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, errMalformed
			}
			f.b, b = v, b[m:]
		case protowire.Fixed32Type:
			v, m := protowire.ConsumeFixed32(b)
			if m < 0 {
				return nil, errMalformed
			}
			f.v, b = uint64(v), b[m:]
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				return nil, errMalformed
			}
			f.v, b = v, b[m:]
		default:
			return nil, errMalformed
		}
		out = append(out, f)
	}
	return out, nil
}

func str(fs []field, num int) string {
	for _, f := range fs {
		if f.num == num && f.typ == protowire.BytesType {
			return string(f.b)
		}
	}
	return ""
}

func child(fs []field, num int) ([]field, bool) {
	for _, f := range fs {
		if f.num == num && f.typ == protowire.BytesType {
			c, err := fields(f.b)
			return c, err == nil
		}
	}
	return nil, false
}

func children(fs []field, num int) [][]field {
	var out [][]field
	for _, f := range fs {
		if f.num == num && f.typ == protowire.BytesType {
			if c, err := fields(f.b); err == nil {
				out = append(out, c)
			}
		}
	}
	return out
}

func raw(fs []field, num int) []byte {
	for _, f := range fs {
		if f.num == num && f.typ == protowire.BytesType {
			return f.b
		}
	}
	return nil
}

func boolean(fs []field, num int) bool {
	for _, f := range fs {
		if f.num == num && f.typ == protowire.VarintType {
			return f.v != 0
		}
	}
	return false
}

// DecodeClient 读一条 RunInferenceClientMessage。只认得出 oneof 里的已知分支才算成功，
// 免得把别的消息（AgentClientMessage）误认成这里的。
func DecodeClient(b []byte) (ClientMessage, error) {
	fs, err := fields(b)
	if err != nil {
		return ClientMessage{}, err
	}
	for _, f := range fs {
		if f.typ != protowire.BytesType {
			continue
		}
		inner, err := fields(f.b)
		if err != nil {
			return ClientMessage{}, err
		}
		switch f.num {
		case ClientRunRequest:
			model, ok := child(inner, 3)
			if !ok || str(model, 1) == "" {
				return ClientMessage{}, fmt.Errorf("run_request without requested_model")
			}
			return ClientMessage{Kind: f.num, Run: &RunRequest{ConversationID: str(inner, 1), ModelID: str(model, 1), MaxMode: boolean(model, 2)}}, nil
		case ClientInvokeModel:
			inv, err := decodeInvoke(inner)
			if err != nil {
				return ClientMessage{}, err
			}
			return ClientMessage{Kind: f.num, Invoke: inv}, nil
		case ClientCancelInvocation:
			return ClientMessage{Kind: f.num, CancelID: str(inner, 1)}, nil
		case ClientFinishRun:
			return ClientMessage{Kind: f.num, Aborted: boolean(inner, 1)}, nil
		case ClientHeartbeat, ClientAgentEvent, ClientRunRequestLog, ClientResolveSubagents, ClientFirstCallTimings:
			return ClientMessage{Kind: f.num}, nil
		}
	}
	return ClientMessage{}, errMalformed
}

func decodeInvoke(fs []field) (*Invoke, error) {
	inv := &Invoke{ID: str(fs, 1)}
	if inv.ID == "" {
		return nil, fmt.Errorf("invoke_model without invocation_id")
	}
	if _, ok := child(fs, 5); ok {
		inv.Unsupported = "delta_base"
	}
	if c, ok := child(fs, 7); ok && len(c) > 0 {
		inv.Unsupported = "compressed_request"
	}
	req, ok := child(fs, 2)
	if !ok {
		if inv.Unsupported == "" {
			inv.Unsupported = "empty request"
		}
		return inv, nil
	}
	var system []string
	for _, m := range children(req, 1) {
		msgs, sys := coreMessage(m)
		if sys != "" {
			system = append(system, sys)
		}
		inv.Messages = append(inv.Messages, msgs...)
	}
	inv.System = strings.Join(system, "\n\n")
	for _, t := range children(req, 2) {
		tool := provider.Tool{Name: str(t, 1), Description: str(t, 2)}
		tool.Parameters = structJSON(raw(t, 3))
		if c, ok := child(t, 4); ok && len(tool.Parameters) <= 2 {
			// 自定义格式（自由文本）工具：给模型一个字符串参数。
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":` + jsonString(str(c, 2)) + `}},"required":["input"]}`)
		}
		tool.Parameters = objectSchema(tool.Name, tool.Parameters)
		inv.Tools = append(inv.Tools, tool)
	}
	return inv, nil
}

// objectSchema 保证工具参数是顶层 type:"object" 的 JSON Schema。
// Cursor 3.26 下发的 Struct 是 {"jsonSchema": <schema>}，顶层没有 type，
// OpenAI 兼容端会以 “schema must be a JSON Schema of 'type: "object"', got 'type: null'” 拒绝整轮。
func objectSchema(name string, params json.RawMessage) json.RawMessage {
	if len(params) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil || m == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if t, _ := m["type"].(string); t == "object" {
		return params
	}
	// Cursor 3.26 把真正的 schema 包在 {"jsonSchema": ...} 里（对象或 JSON 字符串）。
	if js, ok := m["jsonSchema"]; ok && m["type"] == nil {
		switch v := js.(type) {
		case map[string]any:
			b, err := json.Marshal(v)
			if err == nil {
				return objectSchema(name, b)
			}
		case string:
			return objectSchema(name, json.RawMessage(v))
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slog.Debug("本地循环工具参数顶层不是 object，已补齐", "tool", name, "keys", strings.Join(keys, ","))
	if _, ok := m["type"]; ok && m["type"] != nil {
		// 顶层是别的类型：包一层，模型只能传对象。
		m = map[string]any{"type": "object", "properties": map[string]any{"input": m}}
	} else {
		m["type"] = "object"
		if _, ok := m["properties"]; !ok {
			m["properties"] = map[string]any{}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return out
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func structJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	var s structpb.Struct
	if err := proto.Unmarshal(b, &s); err != nil {
		return nil
	}
	out, err := protojson.Marshal(&s)
	if err != nil {
		return nil
	}
	return out
}

func valueText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var v structpb.Value
	if err := proto.Unmarshal(b, &v); err != nil {
		return ""
	}
	if s, ok := v.Kind.(*structpb.Value_StringValue); ok {
		return s.StringValue
	}
	out, err := protojson.Marshal(&v)
	if err != nil {
		return ""
	}
	return string(out)
}

// contentParts 读 InferenceContentParts：文字拼起来，图片收集。
func contentParts(parts [][]field) (string, []provider.Image) {
	var text []string
	var images []provider.Image
	for _, p := range parts {
		if t, ok := child(p, 1); ok {
			text = append(text, str(t, 1))
		}
		if img, ok := child(p, 2); ok {
			if im, ok := decodeImage(str(img, 1), str(img, 2)); ok {
				images = append(images, im)
			}
		}
		if file, ok := child(p, 3); ok {
			mt := str(file, 2)
			if strings.HasPrefix(mt, "image/") {
				if im, ok := decodeImage(str(file, 1), mt); ok {
					images = append(images, im)
				}
			} else if strings.HasPrefix(mt, "text/") {
				if b, err := base64.StdEncoding.DecodeString(str(file, 1)); err == nil {
					text = append(text, string(b))
				}
			}
		}
	}
	return strings.Join(text, ""), images
}

func decodeImage(data, mime string) (provider.Image, bool) {
	if strings.HasPrefix(data, "data:") {
		if i := strings.Index(data, ","); i > 0 {
			head := data[5:i]
			if mime == "" {
				mime = strings.TrimSuffix(head, ";base64")
			}
			data = data[i+1:]
		}
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(data); err != nil {
			return provider.Image{}, false
		}
	}
	if mime == "" {
		mime = "image/png"
	}
	return provider.Image{MIME: mime, Data: b}, true
}

// coreMessage 把一条 InferenceCoreMessage 变成 provider 消息；系统消息单独返回。
func coreMessage(fs []field) ([]provider.Message, string) {
	var role uint64
	for _, f := range fs {
		if f.num == 1 && f.typ == protowire.VarintType {
			role = f.v
		}
	}
	text := str(fs, 2)
	var images []provider.Image
	if parts, ok := child(fs, 3); ok {
		t, im := contentParts(children(parts, 1))
		text += t
		images = im
	}
	switch role {
	case roleSystem:
		return nil, text
	case roleUser:
		return []provider.Message{{Role: "user", Content: text, Images: images}}, ""
	case roleAssistant:
		m := provider.Message{Role: "assistant", Content: text}
		if m.Content == "" {
			for _, f := range fs {
				if f.num == 17 && f.typ == protowire.BytesType {
					m.Content += string(f.b)
				}
			}
		}
		for _, rp := range children(fs, 7) {
			m.Reasoning += str(rp, 2)
			if sig := str(rp, 3); sig != "" {
				m.ReasoningSignature = sig
				m.ReasoningBy = str(rp, 5)
			}
		}
		for _, tc := range children(fs, 4) {
			args := str(tc, 4)
			if args == "" {
				args = string(structJSON(raw(tc, 3)))
			}
			if args == "" {
				args = "{}"
			}
			m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: str(tc, 1), Name: str(tc, 2), Arguments: args})
		}
		return []provider.Message{m}, ""
	case roleTool:
		var out []provider.Message
		if content, ok := child(fs, 6); ok {
			for _, p := range children(content, 1) {
				m := provider.Message{Role: "tool", ToolCallID: str(p, 1), Content: valueText(raw(p, 3)), IsError: boolean(p, 4) || boolean(p, 7)}
				if t, im := contentParts(children(p, 5)); t != "" || len(im) > 0 {
					if m.Content == "" {
						m.Content = t
					}
					m.Images = im
				}
				out = append(out, m)
			}
		}
		return out, ""
	}
	if text != "" {
		return []provider.Message{{Role: "user", Content: text, Images: images}}, ""
	}
	return nil, ""
}

// ---- 服务端消息 ----

func msg(fieldNum int, inner []byte) []byte {
	b := protowire.AppendTag(nil, protowire.Number(fieldNum), protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

func appendStr(b []byte, num int, s string) []byte {
	b = protowire.AppendTag(b, protowire.Number(num), protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendBool(b []byte, num int, v bool) []byte {
	if !v {
		return b
	}
	b = protowire.AppendTag(b, protowire.Number(num), protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}

func appendInt(b []byte, num int, v int) []byte {
	b = protowire.AppendTag(b, protowire.Number(num), protowire.VarintType)
	return protowire.AppendVarint(b, uint64(v))
}

// Heartbeat 是 RunInferenceServerMessage.heartbeat。
func Heartbeat() []byte { return msg(1, nil) }

type ReadyInfo struct {
	ModelID       string
	MaxMode       bool
	DisplayName   string
	ContextTokens int
}

// RunReady 回 run_ready。prompt_model_metadata 按客户端自带的“未归类模型”默认填写
// （vendor unknown、prompt_version latest、persona anonymous），自称用模型的显示名。
func RunReady(info ReadyInfo) []byte {
	model := appendStr(nil, 1, info.ModelID)
	model = appendBool(model, 2, info.MaxMode)
	var meta []byte
	meta = appendStr(meta, 1, "unknown")
	meta = appendStr(meta, 2, "latest")
	if info.ContextTokens > 0 {
		meta = appendInt(meta, 32, info.ContextTokens)
	}
	meta = appendInt(meta, 33, 300000)
	meta = appendStr(meta, 35, "anonymous")
	meta = appendBool(meta, 41, true)
	meta = appendBool(meta, 42, true)
	meta = appendStr(meta, 44, info.DisplayName)
	for _, mode := range []string{"agent", "chat", "plan", "debug"} {
		meta = protowire.AppendTag(meta, 46, protowire.BytesType)
		meta = protowire.AppendBytes(meta, appendStr(nil, 1, mode))
	}
	meta = protowire.AppendTag(meta, 48, protowire.BytesType)
	meta = protowire.AppendBytes(meta, appendStr(nil, 1, "AskQuestion"))

	var r []byte
	r = protowire.AppendTag(r, 1, protowire.BytesType)
	r = protowire.AppendBytes(r, model)
	r = appendStr(r, 3, info.DisplayName)
	r = protowire.AppendTag(r, 4, protowire.BytesType)
	r = protowire.AppendBytes(r, meta)
	r = appendBool(r, 7, true)  // accepts_client_heartbeat
	r = appendBool(r, 13, true) // accepts_aborted_finish
	return msg(2, r)
}

// Response 把一个 InferenceStreamResponse 包成 invocation_response。
func Response(invocationID string, resp []byte) []byte {
	b := appendStr(nil, 1, invocationID)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendBytes(b, resp)
	return msg(3, b)
}

// End 回 invocation_end；code 非 0 时带错误（Connect 错误码）。
func End(invocationID string, code int, message string) []byte {
	b := appendStr(nil, 1, invocationID)
	if code != 0 {
		e := appendInt(nil, 1, code)
		e = appendStr(e, 2, message)
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, e)
	}
	return msg(4, b)
}

// InferenceStreamResponse 各分支
func TextDelta(s string) []byte { return msg(1, appendStr(nil, 1, s)) }
func TextFinal() []byte         { return msg(1, appendBool(appendStr(nil, 1, ""), 2, true)) }
func Thinking(text, signature, model string) []byte {
	b := appendStr(nil, 1, text)
	if signature != "" {
		b = appendStr(b, 2, signature)
	}
	if model != "" {
		b = appendStr(b, 5, model)
	}
	return msg(9, b)
}
func ToolCall(id, name, args string, index int) []byte {
	b := appendStr(nil, 1, id)
	b = appendStr(b, 2, name)
	b = appendStr(b, 3, args)
	b = appendBool(b, 4, true)
	b = appendInt(b, 5, index)
	return msg(2, b)
}
func Usage(prompt, completion int) []byte {
	b := appendInt(nil, 1, prompt)
	b = appendInt(b, 2, completion)
	b = appendInt(b, 3, prompt+completion)
	return msg(3, b)
}
func ExtendedUsage(input, output, cacheRead int) []byte {
	b := appendInt(nil, 1, input)
	b = appendInt(b, 2, output)
	b = appendInt(b, 3, cacheRead)
	b = appendInt(b, 4, 0)
	b = appendInt(b, 5, 0)
	return msg(5, b)
}
func InvocationIDInfo(id string) []byte { return msg(7, appendStr(nil, 1, id)) }

// ResponseInfo 回这一轮的完整助手消息（InferenceResponseInfo）。
func ResponseInfo(id, model string, createdAtMs int64, reply provider.Message, reasoningBy string) []byte {
	m := appendStr(nil, 1, id+"-0")
	m = appendInt(m, 2, roleAssistant)
	if reply.Content != "" {
		m = appendStr(m, 3, reply.Content)
	}
	for _, tc := range reply.ToolCalls {
		t := appendStr(nil, 1, tc.ID)
		t = appendStr(t, 2, tc.Name)
		var args map[string]any
		if json.Unmarshal([]byte(tc.Arguments), &args) == nil {
			if s, err := structpb.NewStruct(args); err == nil {
				if sb, err := proto.Marshal(s); err == nil {
					t = protowire.AppendTag(t, 3, protowire.BytesType)
					t = protowire.AppendBytes(t, sb)
				}
			}
		}
		t = appendStr(t, 4, tc.Arguments)
		m = protowire.AppendTag(m, 4, protowire.BytesType)
		m = protowire.AppendBytes(m, t)
	}
	if reply.Reasoning != "" || reply.ReasoningSignature != "" {
		rp := appendStr(nil, 2, reply.Reasoning)
		if reply.ReasoningSignature != "" {
			rp = appendStr(rp, 3, reply.ReasoningSignature)
		}
		if reasoningBy != "" {
			rp = appendStr(rp, 5, reasoningBy)
		}
		m = protowire.AppendTag(m, 6, protowire.BytesType)
		m = protowire.AppendBytes(m, rp)
	}
	b := appendStr(nil, 1, id)
	b = appendStr(b, 2, model)
	b = protowire.AppendTag(b, 3, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(createdAtMs))
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendBytes(b, m)
	return msg(4, b)
}
