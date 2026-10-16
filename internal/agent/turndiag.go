package agent

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// 回合开始的诊断：记下 Cursor 发来的是哪种动作、带了哪些字段，以及模式和工具为什么回落到默认值。
// 只记字段名、计数、id 和枚举名；不记用户正文、文件内容或任何密钥。日志关键字 turn_action。

const (
	diagMaxFields = 48
	diagMaxDepth  = 3
	diagMaxID     = 80
)

type turnDiag struct {
	Action        string
	HasUser       bool
	Mode          string
	HasReqCtx     bool
	HasEnv        bool
	MCPTools      int
	CatalogTools  int
	Conversation  string
	Turn          int
	Messages      int
	LastRole      string
	LastToolCalls int
	Fallback      []string
	Fields        []string
	IDs           []string
}

// actionName 返回 ConversationAction 里 oneof 的分支名，例如 ResumeAction；没有动作时为 none。
func actionName(action *cursorpb.ConversationAction) string {
	inner := action.GetAction()
	if inner == nil {
		return "none"
	}
	name := fmt.Sprintf("%T", inner)
	name = name[strings.LastIndex(name, ".")+1:]
	return strings.TrimPrefix(name, "ConversationAction_")
}

// fieldSummary 列出消息里已设置的字段路径（只到 diagMaxDepth 层、最多 diagMaxFields 个），
// 并收集名字以 _id 结尾的字符串字段的值（截断）。正文类字段只记出现与否和长度。
func fieldSummary(msg proto.Message) ([]string, []string) {
	var fields, ids []string
	if msg == nil {
		return nil, nil
	}
	var walk func(m protoreflect.Message, prefix string, depth int)
	walk = func(m protoreflect.Message, prefix string, depth int) {
		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			if len(fields) >= diagMaxFields {
				return false
			}
			path := prefix + string(fd.Name())
			switch {
			case fd.IsList():
				fields = append(fields, fmt.Sprintf("%s[%d]", path, v.List().Len()))
			case fd.IsMap():
				fields = append(fields, fmt.Sprintf("%s{%d}", path, v.Map().Len()))
			case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
				fields = append(fields, path)
				if depth < diagMaxDepth {
					walk(v.Message(), path+".", depth+1)
				}
			case fd.Kind() == protoreflect.StringKind:
				s := v.String()
				if strings.HasSuffix(string(fd.Name()), "_id") || string(fd.Name()) == "id" {
					if len(s) > diagMaxID {
						s = s[:diagMaxID]
					}
					ids = append(ids, path+"="+s)
					fields = append(fields, path)
				} else {
					fields = append(fields, fmt.Sprintf("%s(len=%d)", path, len(s)))
				}
			case fd.Kind() == protoreflect.BytesKind:
				fields = append(fields, fmt.Sprintf("%s(bytes=%d)", path, len(v.Bytes())))
			case fd.Kind() == protoreflect.EnumKind:
				ev := fd.Enum().Values().ByNumber(v.Enum())
				name := fmt.Sprint(v.Enum())
				if ev != nil {
					name = string(ev.Name())
				}
				fields = append(fields, path+"="+name)
			default:
				fields = append(fields, path)
			}
			return true
		})
	}
	walk(msg.ProtoReflect(), "", 0)
	sort.Strings(ids)
	return fields, ids
}

func newTurnDiag(run *cursorpb.AgentRunRequest, reqCtx *cursorpb.RequestContext, mcp int, catalog int, messages []provider.Message) turnDiag {
	action := run.GetAction()
	user := action.GetUserMessageAction().GetUserMessage()
	d := turnDiag{
		Action:       actionName(action),
		HasUser:      user != nil,
		Mode:         user.GetMode().String(),
		HasReqCtx:    reqCtx != nil,
		HasEnv:       reqCtx.GetEnv() != nil,
		MCPTools:     mcp,
		CatalogTools: catalog,
		Conversation: conversationID(run.GetConversationId()),
		Turn:         userTurns(messages),
		Messages:     len(messages),
	}
	if n := len(messages); n > 0 {
		d.LastRole = messages[n-1].Role
		d.LastToolCalls = len(messages[n-1].ToolCalls)
	}
	if user == nil {
		d.Fallback = append(d.Fallback, "没有用户消息：模式取默认值，工具用全部内置工具")
	} else if _, ok := modeTools[user.GetMode()]; !ok {
		d.Fallback = append(d.Fallback, "模式不在已知列表：工具用全部内置工具")
	}
	if reqCtx.GetEnv() == nil {
		d.Fallback = append(d.Fallback, "没有运行环境：MCP 工具和工作区信息为空")
	} else if mcp == 0 {
		d.Fallback = append(d.Fallback, "运行环境里没有 MCP 工具")
	}
	if d.LastRole == "assistant" && d.LastToolCalls == 0 {
		d.Fallback = append(d.Fallback, "最后一条是已完成的助手回复，没有新的用户消息或工具结果")
	}
	d.Fields, d.IDs = fieldSummary(action)
	return d
}

func (d turnDiag) log() {
	slog.Info("turn_action 回合动作诊断",
		"action", d.Action,
		"has_user", d.HasUser,
		"mode", d.Mode,
		"request_context", d.HasReqCtx,
		"env", d.HasEnv,
		"mcp_tools", d.MCPTools,
		"tools", d.CatalogTools,
		"conversation", d.Conversation,
		"turn", d.Turn,
		"messages", d.Messages,
		"last_role", d.LastRole,
		"last_tool_calls", d.LastToolCalls,
		"fallback", strings.Join(d.Fallback, "；"),
		"fields", strings.Join(d.Fields, ","),
		"ids", strings.Join(d.IDs, ","),
	)
}
