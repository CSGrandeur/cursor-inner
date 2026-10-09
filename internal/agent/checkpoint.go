package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"

	"google.golang.org/protobuf/proto"
)

// blobID 是 blob 内容的 SHA-256，32 字节。Cursor 和 cursor-byok 都按这个校验。
func blobID(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

type storedBlob struct {
	ID   []byte
	Data []byte
}

type checkpointBuild struct {
	State *cursorpb.ConversationStateStructure
	Blobs []storedBlob
}

type wirePart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     string          `json:"result,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	Image      string          `json:"image,omitempty"`
	MIMEType   string          `json:"mimeType,omitempty"`
}

type wireDoc struct {
	Role    string     `json:"role"`
	Content []wirePart `json:"content"`
	ID      string     `json:"id"`
}

func writeCheckpoint(system, requestID string, messages []provider.Message, mode cursorpb.AgentMode, window, used int, summary string, summaries uint32, known map[string]struct{}, prev *cursorpb.ConversationStateStructure, prevBlobs map[string][]byte) (checkpointBuild, error) {
	if known == nil {
		known = map[string]struct{}{}
	}
	var blobs []storedBlob
	put := func(data []byte) ([]byte, error) {
		id := blobID(data)
		key := string(id)
		if _, ok := known[key]; !ok {
			blobs = append(blobs, storedBlob{ID: append([]byte(nil), id...), Data: append([]byte(nil), data...)})
			known[key] = struct{}{}
		}
		return id, nil
	}
	roots, err := encodeRoots(system, messages, prev, prevBlobs, put)
	if err != nil {
		return checkpointBuild{}, err
	}
	var turns [][]byte
	for i, turn := range splitTurns(messages) {
		if id, ok := reusableTurn(prev, prevBlobs, i, turn); ok {
			turns = append(turns, id)
			continue
		}
		id, err := putTurn(put, turn, requestID)
		if err != nil {
			return checkpointBuild{}, err
		}
		turns = append(turns, id)
	}
	if window <= 0 {
		window = 128000
	}
	if used < 0 {
		used = 0
	}
	state := &cursorpb.ConversationStateStructure{
		RootPromptMessagesJson: roots,
		Turns:                  turns,
		Mode:                   &mode,
		TokenDetails: &cursorpb.ConversationTokenDetails{
			UsedTokens: uint32(min(used, int(^uint32(0)))),
			MaxTokens:  uint32(min(window, int(^uint32(0)))),
		},
	}
	if summary != "" {
		raw, err := proto.Marshal(&cursorpb.ConversationSummary{Summary: summary})
		if err != nil {
			return checkpointBuild{}, err
		}
		state.Summary = raw
		state.SelfSummaryCount = summaries
	}
	return checkpointBuild{State: state, Blobs: blobs}, nil
}

func encodeRoots(system string, messages []provider.Message, prev *cursorpb.ConversationStateStructure, prevBlobs map[string][]byte, put func([]byte) ([]byte, error)) ([][]byte, error) {
	names := toolNames(messages)
	var roots [][]byte
	matched := prefixRoots(system, messages, prev, prevBlobs)
	if system != "" {
		if matched.systemID != nil {
			roots = append(roots, matched.systemID)
		} else {
			raw, err := json.Marshal(wireDoc{Role: "system", ID: "system", Content: []wirePart{{Type: "text", Text: system}}})
			if err != nil {
				return nil, err
			}
			id, err := put(raw)
			if err != nil {
				return nil, err
			}
			roots = append(roots, id)
		}
	}
	roots = append(roots, matched.ids...)
	for i, message := range messages[matched.count:] {
		raw, err := json.Marshal(wireMessage(message, matched.count+i, names))
		if err != nil {
			return nil, err
		}
		id, err := put(raw)
		if err != nil {
			return nil, err
		}
		roots = append(roots, id)
	}
	return roots, nil
}

type matchedRoots struct {
	systemID []byte
	ids      [][]byte
	count    int
}

func prefixRoots(system string, messages []provider.Message, prev *cursorpb.ConversationStateStructure, blobs map[string][]byte) matchedRoots {
	var out matchedRoots
	if prev == nil {
		return out
	}
	ids := prev.GetRootPromptMessagesJson()
	i := 0
	if len(ids) > 0 {
		msg, kind, err := decodeRoot(blobs[string(ids[0])])
		if err == nil && kind == rootSystem {
			if system != "" && msg.Content == system {
				out.systemID = append([]byte(nil), ids[0]...)
			}
			i = 1
		}
	}
	for n := 0; i+n < len(ids) && n < len(messages); n++ {
		msg, kind, err := decodeRoot(blobs[string(ids[i+n])])
		if err != nil || kind != rootMessage || !sameMessage(msg, messages[n]) {
			break
		}
		out.ids = append(out.ids, append([]byte(nil), ids[i+n]...))
		out.count++
	}
	return out
}

func wireMessage(message provider.Message, index int, names map[string]string) wireDoc {
	doc := wireDoc{Role: message.Role, ID: fmt.Sprintf("m%d", index)}
	switch message.Role {
	case "assistant":
		doc.ID = "1"
		if message.Reasoning != "" || message.ReasoningSignature != "" {
			doc.Content = append(doc.Content, wirePart{Type: "reasoning", Text: message.Reasoning, Signature: message.ReasoningSignature})
		}
		if message.Content != "" {
			doc.Content = append(doc.Content, wirePart{Type: "text", Text: message.Content})
		}
		for _, call := range message.ToolCalls {
			doc.Content = append(doc.Content, wirePart{Type: "tool-call", ToolCallID: call.ID, ToolName: call.Name, Args: rawArgs(call.Arguments)})
		}
	case "tool":
		name := names[message.ToolCallID]
		doc.ID = message.ToolCallID
		doc.Content = []wirePart{{Type: "tool-result", ToolCallID: message.ToolCallID, ToolName: name, Result: message.Content, IsError: message.IsError}}
		for _, image := range message.Images {
			doc.Content = append(doc.Content, wirePart{Type: "image", Image: base64.StdEncoding.EncodeToString(image.Data), MIMEType: image.MIME})
		}
	default:
		if message.Content != "" {
			doc.Content = append(doc.Content, wirePart{Type: "text", Text: message.Content})
		}
		for _, image := range message.Images {
			doc.Content = append(doc.Content, wirePart{Type: "image", Image: base64.StdEncoding.EncodeToString(image.Data), MIMEType: image.MIME})
		}
	}
	if doc.Content == nil {
		doc.Content = []wirePart{}
	}
	return doc
}

func rawArgs(arguments string) json.RawMessage {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage("{}")
	}
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(arguments)) == nil {
		return append(json.RawMessage(nil), buf.Bytes()...)
	}
	raw, _ := json.Marshal(arguments)
	return raw
}

func toolNames(messages []provider.Message) map[string]string {
	names := map[string]string{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				names[call.ID] = call.Name
			}
		}
	}
	return names
}

func putTurn(put func([]byte) ([]byte, error), turn []provider.Message, requestID string) ([]byte, error) {
	user := &cursorpb.UserMessage{Text: turn[0].Content}
	userRaw, err := proto.Marshal(user)
	if err != nil {
		return nil, err
	}
	userID, err := put(userRaw)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeTurnSteps(turn)
	if err != nil {
		return nil, err
	}
	var steps [][]byte
	for _, raw := range encoded {
		id, err := put(raw)
		if err != nil {
			return nil, err
		}
		steps = append(steps, id)
	}
	wrapper := &cursorpb.ConversationTurnStructure{Turn: &cursorpb.ConversationTurnStructure_AgentConversationTurn{AgentConversationTurn: &cursorpb.AgentConversationTurnStructure{
		UserMessage: userID,
		Steps:       steps,
		RequestId:   &requestID,
	}}}
	raw, err := proto.Marshal(wrapper)
	if err != nil {
		return nil, err
	}
	return put(raw)
}

func encodeTurnSteps(turn []provider.Message) ([][]byte, error) {
	names := toolNames(turn)
	calls := map[string]provider.ToolCall{}
	for _, message := range turn {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call
		}
	}
	var out [][]byte
	for _, message := range turn[1:] {
		switch message.Role {
		case "assistant":
			if message.Reasoning != "" {
				raw, err := proto.Marshal(&cursorpb.ConversationStep{Message: &cursorpb.ConversationStep_ThinkingMessage{ThinkingMessage: &cursorpb.ThinkingMessage{Text: message.Reasoning}}})
				if err != nil {
					return nil, err
				}
				out = append(out, raw)
			}
			if message.Content != "" {
				raw, err := proto.Marshal(&cursorpb.ConversationStep{Message: &cursorpb.ConversationStep_AssistantMessage{AssistantMessage: &cursorpb.AssistantMessage{Text: message.Content}}})
				if err != nil {
					return nil, err
				}
				out = append(out, raw)
			}
		case "tool":
			call := calls[message.ToolCallID]
			if call.ID == "" {
				call.ID = message.ToolCallID
				call.Name = names[message.ToolCallID]
			}
			card := message.Card
			if card == nil {
				synthesized, err := synthesizeCard(call, message.Content, message.IsError)
				if err != nil {
					continue
				}
				card = synthesized
			}
			raw, err := proto.Marshal(&cursorpb.ConversationStep{Message: &cursorpb.ConversationStep_ToolCall{ToolCall: card}})
			if err != nil {
				return nil, err
			}
			out = append(out, raw)
		}
	}
	return out, nil
}

func synthesizeCard(call provider.ToolCall, result string, isErr bool) (*cursorpb.ToolCall, error) {
	if call.Name == "" {
		return nil, fmt.Errorf("checkpoint: tool result %s has no name", call.ID)
	}
	id := call.ID
	card := &cursorpb.ToolCall{ToolCallId: &id}
	switch strings.ToLower(strings.ReplaceAll(call.Name, "_", "")) {
	case "read":
		path := argString(call.Arguments, "path", "file_path", "filePath")
		read := &cursorpb.ReadToolCall{Args: &cursorpb.ReadToolArgs{Path: path}}
		if isErr {
			read.Result = &cursorpb.ReadToolResult{Result: &cursorpb.ReadToolResult_Error{Error: &cursorpb.ReadToolError{ErrorMessage: result}}}
		} else {
			read.Result = &cursorpb.ReadToolResult{Result: &cursorpb.ReadToolResult_Success{Success: &cursorpb.ReadToolSuccess{Path: path, Output: &cursorpb.ReadToolSuccess_Content{Content: result}}}}
		}
		card.Tool = &cursorpb.ToolCall_ReadToolCall{ReadToolCall: read}
	case "grep":
		pattern := argString(call.Arguments, "pattern")
		card.Tool = &cursorpb.ToolCall_GrepToolCall{GrepToolCall: &cursorpb.GrepToolCall{Args: &cursorpb.GrepArgs{Pattern: pattern, ToolCallId: call.ID}}}
	case "glob":
		pattern := argString(call.Arguments, "glob_pattern", "pattern")
		card.Tool = &cursorpb.ToolCall_GlobToolCall{GlobToolCall: &cursorpb.GlobToolCall{Args: &cursorpb.GlobToolArgs{GlobPattern: pattern}}}
	case "delete":
		path := argString(call.Arguments, "path", "file_path", "filePath")
		card.Tool = &cursorpb.ToolCall_DeleteToolCall{DeleteToolCall: &cursorpb.DeleteToolCall{Args: &cursorpb.DeleteArgs{Path: path, ToolCallId: call.ID}}}
	case "strreplace", "write", "editnotebook":
		path := argString(call.Arguments, "path", "file_path", "filePath", "target_notebook")
		card.Tool = &cursorpb.ToolCall_EditToolCall{EditToolCall: &cursorpb.EditToolCall{Args: &cursorpb.EditArgs{Path: path}}}
	case "shell", "bash":
		command := argString(call.Arguments, "command")
		card.Tool = &cursorpb.ToolCall_ShellToolCall{ShellToolCall: &cursorpb.ShellToolCall{Args: &cursorpb.ShellArgs{Command: command, ToolCallId: call.ID}}}
	default:
		return nil, fmt.Errorf("checkpoint: tool %s is not a shell command and has no card", call.Name)
	}
	return card, nil
}

func argString(raw string, keys ...string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := fields[key].(string); ok {
			return text
		}
	}
	return ""
}

func reusableTurn(prev *cursorpb.ConversationStateStructure, blobs map[string][]byte, index int, turn []provider.Message) ([]byte, bool) {
	if prev == nil || index >= len(prev.GetTurns()) || len(turn) == 0 {
		return nil, false
	}
	raw, ok := blobs[string(prev.Turns[index])]
	if !ok {
		return nil, false
	}
	var wrapper cursorpb.ConversationTurnStructure
	if proto.Unmarshal(raw, &wrapper) != nil || wrapper.GetAgentConversationTurn() == nil {
		return nil, false
	}
	agentTurn := wrapper.GetAgentConversationTurn()
	userRaw, ok := blobs[string(agentTurn.GetUserMessage())]
	if !ok {
		return nil, false
	}
	var user cursorpb.UserMessage
	if proto.Unmarshal(userRaw, &user) != nil || user.GetText() != turn[0].Content {
		return nil, false
	}
	if countSteps(turn) > len(agentTurn.GetSteps()) {
		return nil, false
	}
	return append([]byte(nil), prev.Turns[index]...), true
}

func countSteps(turn []provider.Message) int {
	n := 0
	for _, message := range turn[1:] {
		switch message.Role {
		case "assistant":
			if message.Reasoning != "" {
				n++
			}
			if message.Content != "" {
				n++
			}
		case "tool":
			n++
		}
	}
	return n
}

func splitTurns(messages []provider.Message) [][]provider.Message {
	var turns [][]provider.Message
	var cur []provider.Message
	for _, message := range messages {
		if message.Role == "user" && len(cur) > 0 {
			turns = append(turns, cur)
			cur = nil
		}
		if message.Role == "user" || len(cur) > 0 {
			cur = append(cur, message)
		}
	}
	if len(cur) > 0 && cur[0].Role == "user" {
		turns = append(turns, cur)
	}
	return turns
}

func importPrefetched(blobs []*cursorpb.PreFetchedBlob) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, blob := range blobs {
		if blob == nil {
			continue
		}
		id := blobID(blob.GetValue())
		if string(id) != string(blob.GetId()) {
			return nil, fmt.Errorf("prefetched blob hash mismatch")
		}
		out[string(id)] = append([]byte(nil), blob.GetValue()...)
	}
	return out, nil
}

const (
	rootSystem = iota
	rootMessage
)

func restoreMessages(state *cursorpb.ConversationStateStructure, blobs map[string][]byte) ([]provider.Message, error) {
	if state == nil {
		return nil, nil
	}
	var out []provider.Message
	for _, id := range state.GetRootPromptMessagesJson() {
		raw, ok := blobs[string(id)]
		if !ok {
			return nil, fmt.Errorf("checkpoint: missing root blob")
		}
		message, kind, err := decodeRoot(raw)
		if err != nil {
			return nil, err
		}
		if kind == rootSystem {
			continue
		}
		out = append(out, message)
	}
	return out, nil
}

func decodeRoot(raw []byte) (provider.Message, int, error) {
	if len(raw) == 0 {
		return provider.Message{}, 0, fmt.Errorf("checkpoint: empty root")
	}
	var doc struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		ID      string          `json:"id"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Role == "" {
		return provider.Message{}, 0, fmt.Errorf("checkpoint: root is not a Cursor message")
	}
	if len(doc.Content) > 0 && doc.Content[0] == '"' {
		return provider.Message{}, 0, fmt.Errorf("checkpoint: root content is not a Cursor content array")
	}
	var parts []wirePart
	if len(doc.Content) > 0 && string(doc.Content) != "null" {
		if err := json.Unmarshal(doc.Content, &parts); err != nil {
			return provider.Message{}, 0, fmt.Errorf("checkpoint: root content is not a Cursor content array")
		}
	}
	if doc.Role == "system" {
		return provider.Message{Role: "system", Content: partText(parts)}, rootSystem, nil
	}
	message := provider.Message{Role: doc.Role}
	switch doc.Role {
	case "assistant":
		var text strings.Builder
		for _, part := range parts {
			switch part.Type {
			case "text":
				text.WriteString(part.Text)
			case "reasoning":
				message.Reasoning += part.Text
				if part.Signature != "" {
					message.ReasoningSignature = part.Signature
				}
			case "tool-call":
				message.ToolCalls = append(message.ToolCalls, provider.ToolCall{ID: part.ToolCallID, Name: part.ToolName, Arguments: argsText(part.Args)})
			}
		}
		message.Content = text.String()
	case "tool":
		if len(parts) == 0 || parts[0].Type != "tool-result" {
			return provider.Message{}, 0, fmt.Errorf("checkpoint: tool root has no tool-result")
		}
		message.ToolCallID = parts[0].ToolCallID
		message.Content = parts[0].Result
		message.IsError = parts[0].IsError
		for _, part := range parts[1:] {
			if part.Type != "image" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(part.Image)
			if err != nil {
				return provider.Message{}, 0, fmt.Errorf("checkpoint: image is not base64")
			}
			message.Images = append(message.Images, provider.Image{MIME: part.MIMEType, Data: data})
		}
	default:
		var text strings.Builder
		for _, part := range parts {
			switch part.Type {
			case "text":
				text.WriteString(part.Text)
			case "image":
				data, err := base64.StdEncoding.DecodeString(part.Image)
				if err != nil {
					return provider.Message{}, 0, fmt.Errorf("checkpoint: image is not base64")
				}
				message.Images = append(message.Images, provider.Image{MIME: part.MIMEType, Data: data})
			}
		}
		message.Content = text.String()
	}
	return message, rootMessage, nil
}

func partText(parts []wirePart) string {
	var text strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func argsText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func sameMessage(a, b provider.Message) bool {
	if a.Role != b.Role || a.Content != b.Content || a.Reasoning != b.Reasoning || a.ReasoningSignature != b.ReasoningSignature || a.ToolCallID != b.ToolCallID || a.IsError != b.IsError || len(a.ToolCalls) != len(b.ToolCalls) || len(a.Images) != len(b.Images) {
		return false
	}
	for i := range a.ToolCalls {
		if a.ToolCalls[i].ID != b.ToolCalls[i].ID || a.ToolCalls[i].Name != b.ToolCalls[i].Name || a.ToolCalls[i].Arguments != b.ToolCalls[i].Arguments {
			return false
		}
	}
	for i := range a.Images {
		if a.Images[i].MIME != b.Images[i].MIME || !bytes.Equal(a.Images[i].Data, b.Images[i].Data) {
			return false
		}
	}
	return true
}
