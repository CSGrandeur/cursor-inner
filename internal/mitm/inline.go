package mitm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

// inline 在行内编辑或终端 Cmd+K 带了自定义模型时本地回答。官方模型仍原样转发。
func (s *Server) inline(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	plain, err := protox.Plain(body, r.Header.Get("Content-Encoding"))
	if err != nil {
		withBody(r, body)
		s.forward(w, r)
		return
	}
	id := leakedModel(plain, s.entries())
	if id == "" && protoString(plain, "default") {
		id = s.selectedModel()
	}
	model, ok := s.lookup(id)
	if id == "" || !ok {
		slog.Debug("行内编辑转发给官方", "path", r.URL.Path)
		withBody(r, body)
		s.forward(w, r)
		return
	}
	name := display(model)
	kind := "行内编辑"
	if strings.Contains(r.URL.Path, "Terminal") {
		kind = "终端命令"
	}
	started := time.Now()
	slog.Info("▶ "+name, "kind", kind, "path", r.URL.Path)
	s.countLocal()
	d, err := dialer.ForModel(s.proxy(), model.UseProxy)
	if err != nil {
		slog.Error("✗ "+name, "kind", kind, "reason", provider.Explain(err), "error", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/connect+proto")
	w.Header().Set("connect-protocol-version", "1")
	w.WriteHeader(http.StatusOK)
	flush(w)
	text, err := inlineText(r.Context(), model, d, plain, id)
	if err != nil {
		slog.Error("✗ "+name, "kind", kind, "reason", provider.Explain(err), "error", err)
		_, _ = w.Write(protox.EndError(err.Error()))
		flush(w)
		return
	}
	slog.Info("✓ "+name, "kind", kind, "seconds", fmt.Sprintf("%.1f", time.Since(started).Seconds()))
	_, _ = w.Write(protox.Frame(0, inlineFrame(r.URL.Path, text)))
	_, _ = w.Write(protox.EndStream())
	flush(w)
}

func protoString(body []byte, want string) bool {
	return protoStringDepth(body, want, 0)
}

func protoStringDepth(body []byte, want string, depth int) bool {
	if depth > 6 {
		return false
	}
	fields, err := protox.Fields(body)
	if err != nil {
		return false
	}
	for _, field := range fields {
		if field.Wire != 2 || len(field.Raw) == 0 {
			continue
		}
		if string(field.Raw) == want {
			return true
		}
		if protoStringDepth(field.Raw, want, depth+1) {
			return true
		}
	}
	return false
}

func inlineHint(raw []byte) (string, bool) {
	if !utf8.Valid(raw) || len(raw) < 3 || len(raw) > 80 || bytes.ContainsAny(raw, "\n\r\x00 ./\\") {
		return "", false
	}
	text := string(raw)
	lower := strings.ToLower(text)
	if strings.Contains(lower, "key") || strings.Contains(lower, "bearer") || strings.Contains(lower, "sk-") {
		return "", false
	}
	if text != "default" && allHex(text) && len(text) >= 32 {
		return "", false
	}
	return text, true
}

func allHex(text string) bool {
	for _, r := range text {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return text != ""
}

func display(model config.Model) string {
	if model.DisplayName != "" {
		return model.DisplayName
	}
	return model.ID
}

func inlineText(ctx context.Context, model config.Model, d dialer.Func, plain []byte, modelID string) (string, error) {
	prompt := promptFrom(plain, modelID)
	if prompt == "" {
		prompt = "The user invoked inline edit."
	}
	reply, err := provider.Chat(ctx, model, d, "Reply with the replacement text or the command. Do not explain.", []provider.Message{{
		Role: "user", Content: prompt,
	}}, nil, func(string) error { return nil }, nil)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(reply.Content)
	if text == "" {
		text = " "
	}
	return text, nil
}

func inlineFrame(path, text string) []byte {
	if strings.Contains(path, "StreamTerminalCmdK") {
		return inlineTerminalFrame(text)
	}
	if strings.Contains(path, "StreamCmdK") || strings.Contains(path, "SlashEdit") {
		return inlineEditFrame(text)
	}
	return inlineChatFrame(text)
}

func inlineTerminalFrame(text string) []byte {
	command := protox.AppendString(nil, 1, text)
	inner := protox.AppendBytes(nil, 1, command)
	return protox.AppendBytes(nil, 1, inner)
}

func inlineEditFrame(text string) []byte {
	stream := protox.AppendString(nil, 1, text)
	inner := protox.AppendBytes(nil, 2, stream)
	return protox.AppendBytes(nil, 1, inner)
}

func inlineChatFrame(text string) []byte {
	chat := protox.AppendString(nil, 1, text)
	inner := protox.AppendBytes(nil, 4, chat)
	return protox.AppendBytes(nil, 1, inner)
}

func promptFrom(body []byte, modelID string) string {
	raw, _ := protox.Unary(body)
	if built := cmdKPrompt(raw); built != "" {
		return built
	}
	var parts []string
	var walk func([]byte, int)
	walk = func(buf []byte, depth int) {
		fields, err := protox.Fields(buf)
		if err != nil {
			return
		}
		for _, field := range fields {
			if field.Wire != 2 || len(field.Raw) == 0 {
				continue
			}
			if text, ok := promptString(field.Raw, modelID); ok {
				parts = append(parts, text)
			}
			if depth < 6 {
				walk(field.Raw, depth+1)
			}
		}
	}
	walk(raw, 0)
	var words []string
	for _, part := range parts {
		if len(part) <= 400 && strings.Contains(part, " ") && !slashCatalog(part) {
			words = append(words, part)
		}
	}
	if len(words) > 0 {
		if len(words) > 4 {
			words = words[len(words)-4:]
		}
		return strings.Join(words, "\n")
	}
	if len(parts) > 8 {
		parts = parts[len(parts)-8:]
	}
	var kept []string
	for _, part := range parts {
		if !slashCatalog(part) {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	text := strings.Join(kept, "\n")
	if len(text) > 12000 {
		text = text[len(text)-12000:]
	}
	return text
}

// cmdKPrompt 只取 CmdKQuery / TerminalCmdKQuery，以及选区和文件路径。
// 规则、斜杠命令目录和其它上下文字段不进提示词。
func cmdKPrompt(raw []byte) string {
	var query string
	var selection []string
	var path string
	for _, item := range protox.Children(raw, 1) {
		ctx := contextItem(item)
		if ctx == nil {
			continue
		}
		if q := queryText(protox.Child(ctx, 6)); q != "" {
			query = q
		}
		if q := queryText(protox.Child(ctx, 15)); q != "" {
			query = q
		}
		for _, line := range protox.Children(protox.Child(ctx, 4), 1) {
			if text, ok := plainText(line); ok {
				selection = append(selection, text)
			}
		}
		if p := protox.StringField(protox.Child(ctx, 5), 1); p != "" && !strings.Contains(p, "\n") {
			path = p
		}
	}
	if query == "" {
		if text, ok := promptString(protox.Child(raw, 18), ""); ok && !slashCatalog(text) {
			query = text
		}
	}
	if query == "" {
		return ""
	}
	var b strings.Builder
	if path != "" {
		b.WriteString("file: " + path + "\n")
	}
	if len(selection) > 0 {
		b.WriteString("selection:\n")
		b.WriteString(strings.Join(selection, "\n"))
		b.WriteByte('\n')
	}
	b.WriteString(query)
	return b.String()
}

func contextItem(item []byte) []byte {
	if queryText(protox.Child(item, 6)) != "" || queryText(protox.Child(item, 15)) != "" || protox.Child(item, 4) != nil {
		return item
	}
	inner := protox.Child(item, 1)
	if queryText(protox.Child(inner, 6)) != "" || queryText(protox.Child(inner, 15)) != "" || protox.Child(inner, 4) != nil {
		return inner
	}
	return nil
}

func queryText(msg []byte) string {
	text, ok := plainText(protox.Child(msg, 1))
	if !ok || slashCatalog(text) {
		return ""
	}
	return text
}

func plainText(raw []byte) (string, bool) {
	if len(raw) == 0 || len(raw) > 8000 || !utf8.Valid(raw) {
		return "", false
	}
	text := string(raw)
	for _, r := range text {
		if r < 0x20 && r != '\n' && r != '\t' {
			return "", false
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

func slashCatalog(text string) bool {
	slash, nonempty := 0, 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonempty++
		if strings.HasPrefix(line, "/") {
			slash++
		}
	}
	return nonempty > 0 && slash*2 >= nonempty
}

func promptString(raw []byte, modelID string) (string, bool) {
	if len(raw) < 8 || len(raw) > 8000 || !utf8.Valid(raw) {
		return "", false
	}
	text := string(raw)
	if text == modelID || strings.TrimSpace(text) == "" {
		return "", false
	}
	for _, r := range text {
		if r < 0x20 && r != '\n' && r != '\t' {
			return "", false
		}
	}
	return text, true
}
