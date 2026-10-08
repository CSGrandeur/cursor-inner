package agent

import (
	"context"
	"fmt"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"
)

func (s *Session) semSearch(ctx context.Context, send Emit, call provider.ToolCall) (toolOutcome, error) {
	query, dirs, err, ok := tools.SearchRequest(call)
	if !ok {
		return toolOutcome{}, fmt.Errorf("not semsearch")
	}
	if err != nil {
		return s.argFailure(call, err), nil
	}
	pattern := tools.SearchPattern(query)
	if pattern == "" {
		return s.argFailure(call, fmt.Errorf("SemSearch requires query")), nil
	}
	paths := dirs
	if len(paths) == 0 {
		paths = []string{""}
	}
	var files []string
	for _, dir := range paths {
		text, grepErr := s.clientGrep(ctx, send, pattern, dir, "files_with_matches", 20)
		if grepErr != nil {
			return s.card(send, call, grepErr.Error(), true, toolsUI(call.ID, query, dirs, grepErr.Error()), "")
		}
		for _, snip := range tools.ParseGrepLines(text, false) {
			files = append(files, snip.Path)
			if len(files) >= 20 {
				break
			}
		}
		if len(files) >= 20 {
			break
		}
	}
	var snips []tools.Snippet
	for _, file := range files {
		if len(snips) >= 12 {
			break
		}
		text, grepErr := s.clientGrep(ctx, send, pattern, file, "content", 4)
		if grepErr != nil {
			continue
		}
		for _, snip := range tools.ParseGrepLines(text, true) {
			if snip.Line == "" {
				snip.Line = snip.Path
			}
			snips = append(snips, snip)
			if len(snips) >= 12 {
				break
			}
		}
	}
	text := "No matches for " + query
	if len(snips) > 0 && s.dial != nil {
		reply, chatErr := provider.Chat(ctx, s.Model, s.dial, "Reply with the numbers of the relevant hits only, separated by commas. Do not add paths or explanations.", []provider.Message{{
			Role: "user", Content: "Query: " + query + "\n" + tools.FormatCandidates(snips),
		}}, nil, func(string) error { return nil }, nil)
		if chatErr == nil {
			text = tools.SelectSnippets(snips, reply.Content)
		} else {
			text = tools.SelectSnippets(snips, numbers(len(snips)))
		}
	}
	return s.card(send, call, text, false, toolsUI(call.ID, query, dirs, text), "")
}

func numbers(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprint(i + 1)
	}
	return strings.Join(parts, ",")
}

func toolsUI(id, query string, dirs []string, text string) *cursorpb.ToolCall {
	return &cursorpb.ToolCall{ToolCallId: &id, Tool: &cursorpb.ToolCall_SemSearchToolCall{SemSearchToolCall: &cursorpb.SemSearchToolCall{
		Args:   &cursorpb.SemSearchToolArgs{Query: query, TargetDirectories: dirs},
		Result: &cursorpb.SemSearchToolResult{Result: &cursorpb.SemSearchToolResult_Success{Success: &cursorpb.SemSearchToolSuccess{Results: text}}},
	}}}
}

func (s *Session) clientGrep(ctx context.Context, send Emit, pattern, path, mode string, head int32) (string, error) {
	args := fmt.Sprintf(`{"pattern":%q,"output_mode":%q,"head_limit":%d`, pattern, mode, head)
	if path != "" {
		args += fmt.Sprintf(`,"path":%q`, path)
	}
	args += "}"
	s.nextID++
	exec, ui, pending, err := tools.Request(s.nextID, provider.ToolCall{ID: "sem", Name: "Grep", Arguments: args})
	if err != nil {
		return "", err
	}
	if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: exec}}); err != nil {
		return "", err
	}
	s.track(exec.GetId())
	msg, err := s.await(ctx, exec.GetId())
	s.untrack(exec.GetId())
	if err != nil {
		return "", err
	}
	text, isErr := pending.Result(msg, ui)
	if isErr {
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}
