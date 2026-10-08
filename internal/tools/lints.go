package tools

import (
	"fmt"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func startLints(id uint32, call provider.ToolCall, a args) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	var paths []string
	if raw, ok := a["paths"].([]any); ok {
		for _, item := range raw {
			if path, ok := item.(string); ok && path != "" {
				paths = append(paths, path)
			}
		}
	}
	path := ""
	if len(paths) > 0 {
		path = paths[0]
	}
	exec := &cursorpb.ExecServerMessage{
		Id:     id,
		ExecId: fmt.Sprintf("%s-%d", call.ID, id),
		Message: &cursorpb.ExecServerMessage_DiagnosticsArgs{DiagnosticsArgs: &cursorpb.DiagnosticsArgs{
			Path:       path,
			ToolCallId: call.ID,
		}},
	}
	ui := &cursorpb.ToolCall{
		ToolCallId: &call.ID,
		Tool: &cursorpb.ToolCall_ReadLintsToolCall{ReadLintsToolCall: &cursorpb.ReadLintsToolCall{
			Args: &cursorpb.ReadLintsToolArgs{Paths: paths},
		}},
	}
	return exec, ui, &Pending{Call: call}, nil
}

func lintText(result *cursorpb.DiagnosticsResult) (string, bool) {
	switch v := result.GetResult().(type) {
	case *cursorpb.DiagnosticsResult_Success:
		if len(v.Success.GetDiagnostics()) == 0 {
			return "No linter errors found.", false
		}
		var lines []string
		for _, item := range v.Success.GetDiagnostics() {
			lines = append(lines, fmt.Sprintf("%s: %s", v.Success.GetPath(), item.GetMessage()))
		}
		return strings.Join(lines, "\n"), false
	case *cursorpb.DiagnosticsResult_Error:
		return v.Error.GetError(), true
	case *cursorpb.DiagnosticsResult_Rejected:
		return v.Rejected.GetReason(), true
	case *cursorpb.DiagnosticsResult_FileNotFound:
		return "file not found: " + v.FileNotFound.GetPath(), true
	case *cursorpb.DiagnosticsResult_PermissionDenied:
		return "permission denied: " + v.PermissionDenied.GetPath(), true
	default:
		return "Cursor returned an empty diagnostics result", true
	}
}

func lintUI(result *cursorpb.DiagnosticsResult, text string, isErr bool) *cursorpb.ReadLintsToolResult {
	if isErr {
		return &cursorpb.ReadLintsToolResult{Result: &cursorpb.ReadLintsToolResult_Error{Error: &cursorpb.ReadLintsToolError{ErrorMessage: text}}}
	}
	success := result.GetSuccess()
	items := make([]*cursorpb.DiagnosticItem, 0, len(success.GetDiagnostics()))
	for _, item := range success.GetDiagnostics() {
		items = append(items, &cursorpb.DiagnosticItem{Severity: item.GetSeverity(), Message: item.GetMessage(), Source: item.GetSource(), Code: item.GetCode(), IsStale: item.GetIsStale()})
	}
	return &cursorpb.ReadLintsToolResult{Result: &cursorpb.ReadLintsToolResult_Success{Success: &cursorpb.ReadLintsToolSuccess{
		FileDiagnostics:  []*cursorpb.FileDiagnostics{{Path: success.GetPath(), Diagnostics: items, DiagnosticsCount: int32(len(items))}},
		TotalFiles:       1,
		TotalDiagnostics: success.GetTotalDiagnostics(),
	}}}
}
