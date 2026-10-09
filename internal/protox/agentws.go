package protox

// AgentPayload 是 /agent/v1/run 一条二进制帧里的内容。对话本身仍是 AgentClientMessage。
type AgentPayload struct {
	Kind      string
	RequestID string
	Seq       uint64
	HasSeq    bool
	Message   []byte
}

// ClassifyAgentPayload 认出握手、元数据和一条客户端消息。认不出就当普通帧，调用方原样转发。
func ClassifyAgentPayload(p []byte) AgentPayload {
	fields, err := Fields(p)
	if err != nil || len(fields) == 0 {
		return AgentPayload{Kind: "other"}
	}
	if len(fields) == 1 && fields[0].Num == 1 && fields[0].Wire == 2 && len(fields[0].Raw) <= 8 {
		return AgentPayload{Kind: "hello"}
	}
	switch fields[0].Num {
	case 2:
		return AgentPayload{Kind: "meta", RequestID: StringField(Child(Child(p, 2), 1), 1)}
	case 3:
		item := Child(p, 3)
		out := AgentPayload{Kind: "agent", RequestID: StringField(item, 1), Message: Child(item, 3)}
		for _, f := range mustFields(item) {
			if f.Num == 2 && f.Wire == 0 {
				out.Seq = f.Val
				out.HasSeq = true
			}
		}
		return out
	default:
		return AgentPayload{Kind: "other"}
	}
}

func mustFields(b []byte) []Field {
	fields, err := Fields(b)
	if err != nil {
		return nil
	}
	return fields
}

// EncodeAgentServer 把一条服务端消息包进 Agents 窗口使用的序号帧。
func EncodeAgentServer(requestID string, seq uint64, msg []byte) []byte {
	item := AppendString(nil, 1, requestID)
	item = AppendVarint(item, 2, seq)
	item = AppendBytes(item, 3, msg)
	return AppendBytes(nil, 3, item)
}

// EncodeAgentMetaAck 回写客户端带来的请求编号。服务端确认里这两处是同一个值。
func EncodeAgentMetaAck(requestID string) []byte {
	pair := AppendString(nil, 1, requestID)
	pair = AppendString(pair, 2, requestID)
	return AppendBytes(nil, 2, AppendBytes(nil, 1, pair))
}
