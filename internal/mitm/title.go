package mitm

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"

	"google.golang.org/protobuf/proto"
)

const (
	nameTabPath                    = "/aiserver.v1.AiService/NameTab"
	updateConversationMetadataPath = "/agent.v1.AgentService/UpdateConversationMetadata"
)

// updateConversationMetadata 处理侧栏「重命名」。Cursor 先改本地 store，再异步同步到
// AgentService/UpdateConversationMetadata。本机答过的自定义模型会话 id 官方不认，
// 转发会失败或无意义；仿 cursor-byok，对本机会话直接回空成功。
func (s *Server) updateConversationMetadata(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	plain, err := protox.Plain(body, r.Header.Get("Content-Encoding"))
	if err != nil {
		plain = body
	}
	var req cursorpb.UpdateConversationMetadataRequest
	_ = proto.Unmarshal(plain, &req)
	conv := req.GetConversationId()
	if conv == "" {
		conv = protox.StringField(plain, 1)
	}
	if s.hub != nil && s.hub.KnowsConversation(conv) {
		slog.Debug("本机会话改名，本地回空成功", "conversation", conv, "name", req.GetName())
		s.countLocal()
		writeUnaryProto(w, nil)
		return
	}
	withBody(r, body)
	s.forward(w, r)
}

// nameTab 处理自动起标题。17F 上 Cursor 私有推理关闭时走 AiService/NameTab，
// 请求体常带着自定义模型 id。原先 quietLeak 只 scrub 再转发，官方拿被改写的 id
// 起不出标题。对本机自定义模型改为本地从首条用户消息推导标题，不再转发。
func (s *Server) nameTab(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	enc := r.Header.Get("Content-Encoding")
	plain, err := protox.Plain(body, enc)
	if err != nil {
		plain = body
	}
	entries := s.entries()
	if leakedModel(plain, entries) == "" && leakedModel(body, entries) == "" {
		withBody(r, body)
		s.forward(w, r)
		return
	}
	title := deriveChatTitle(plain)
	if title == "" {
		title = "New Chat"
	}
	slog.Debug("自定义模型对话标题本地生成", "title", title)
	s.countLocal()
	writeUnaryProto(w, protox.AppendString(nil, 1, title))
}

// deriveChatTitle 从 NameTabRequest.messages（field 1）里取第一条非空
// ConversationMessage.text（field 1），截成短标题。对齐 Cursor 本地起名启发式。
// 用宽松扫描而不是一次 Fields 到底：请求体后若夹带无法解析的字节，仍能读到前面的消息。
func deriveChatTitle(nameTabPlain []byte) string {
	b := nameTabPlain
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		num, wire := int(tag>>3), int(tag&7)
		switch wire {
		case 0:
			_, n = binary.Uvarint(b)
			if n <= 0 {
				return ""
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return ""
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return ""
			}
			raw := b[n : n+int(l)]
			b = b[n+int(l):]
			if num != 1 {
				continue
			}
			text := strings.TrimSpace(protox.StringField(raw, 1))
			if text == "" {
				continue
			}
			return shortenTitle(text)
		case 5:
			if len(b) < 4 {
				return ""
			}
			b = b[4:]
		default:
			return ""
		}
	}
	return ""
}

func shortenTitle(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	text = strings.Join(strings.Fields(text), " ")
	const max = 60
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	s := string(runes[:max])
	if i := strings.LastIndexFunc(s, unicode.IsSpace); i > max/2 {
		s = strings.TrimSpace(s[:i])
	}
	return s + "…"
}

func writeUnaryProto(w http.ResponseWriter, body []byte) {
	if body == nil {
		body = []byte{}
	}
	w.Header().Set("Content-Type", "application/proto")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
