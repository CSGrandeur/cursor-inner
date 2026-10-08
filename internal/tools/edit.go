package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

const (
	editRead = iota
	editWrite
)

// editState 记下 StrReplace / Write 在读文件之后、写文件之前算好的前后内容。
type editState struct {
	phase  int
	path   string
	before string
	after  string
}

func startEdit(id uint32, call provider.ToolCall, a args) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	var path string
	var ok bool
	if normalize(call.Name) == "editnotebook" {
		path, ok = a.str("target_notebook")
	} else {
		path, ok = a.str("path", "file_path", "filePath")
	}
	if !ok {
		return nil, nil, nil, fmt.Errorf("%s requires path", call.Name)
	}
	var stream string
	switch normalize(call.Name) {
	case "write":
		contents, ok := a.rawString("contents", "content")
		if !ok {
			return nil, nil, nil, fmt.Errorf("Write requires contents")
		}
		stream = normalizeNewlines(contents)
	case "editnotebook":
		next, ok := a.rawString("new_string")
		if !ok {
			return nil, nil, nil, fmt.Errorf("EditNotebook requires new_string")
		}
		stream = normalizeNewlines(next)
	default:
		if _, ok := a.rawString("old_string"); !ok {
			return nil, nil, nil, fmt.Errorf("StrReplace requires old_string")
		}
		next, ok := a.rawString("new_string")
		if !ok {
			return nil, nil, nil, fmt.Errorf("StrReplace requires new_string")
		}
		stream = normalizeNewlines(next)
	}
	exec := &cursorpb.ExecServerMessage{
		Id:      id,
		ExecId:  fmt.Sprintf("%s-%d", call.ID, id),
		Message: &cursorpb.ExecServerMessage_ReadArgs{ReadArgs: &cursorpb.ReadArgs{Path: path, ToolCallId: call.ID}},
	}
	ui := &cursorpb.ToolCall{
		ToolCallId: &call.ID,
		Tool: &cursorpb.ToolCall_EditToolCall{EditToolCall: &cursorpb.EditToolCall{Args: &cursorpb.EditArgs{
			Path:          path,
			StreamContent: &stream,
		}}},
	}
	return exec, ui, &Pending{Call: call, edit: &editState{path: path}}, nil
}

func (p *Pending) afterRead(msg *cursorpb.ExecClientMessage, ui *cursorpb.ToolCall) (*cursorpb.ExecServerMessage, string, bool) {
	read := msg.GetReadResult()
	if read == nil {
		text := fmt.Sprintf("expected ReadResult for edit tool %s", p.Call.Name)
		failEdit(ui, p.edit.path, text)
		return nil, text, true
	}
	before, err := fileFromRead(p.Call, read)
	if err != nil {
		failEdit(ui, p.edit.path, err.Error())
		return nil, err.Error(), true
	}
	a, err := parseArgs(p.Call.Arguments)
	if err != nil {
		failEdit(ui, p.edit.path, err.Error())
		return nil, err.Error(), true
	}
	var after string
	switch normalize(p.Call.Name) {
	case "write":
		contents, _ := a.rawString("contents", "content")
		after = normalizeNewlines(contents)
	case "editnotebook":
		after, err = editNotebook(a, before)
		if err != nil {
			failEdit(ui, p.edit.path, err.Error())
			return nil, err.Error(), true
		}
	default:
		after, err = replaceString(a, before)
		if err != nil {
			failEdit(ui, p.edit.path, err.Error())
			return nil, err.Error(), true
		}
	}
	p.edit.before = before
	p.edit.after = after
	p.edit.phase = editWrite
	return &cursorpb.ExecServerMessage{Message: &cursorpb.ExecServerMessage_WriteArgs{WriteArgs: &cursorpb.WriteArgs{
		Path:                        p.edit.path,
		FileText:                    after,
		ToolCallId:                  p.Call.ID,
		ReturnFileContentAfterWrite: false,
	}}}, "", false
}

func editNotebook(a args, before string) (string, error) {
	var notebook map[string]any
	if err := json.Unmarshal([]byte(before), &notebook); err != nil {
		return "", fmt.Errorf("invalid notebook JSON: %w", err)
	}
	rawCells, ok := notebook["cells"].([]any)
	if !ok {
		return "", fmt.Errorf("notebook has no cells array")
	}
	idx := 0
	if n := a.int32("cell_idx"); n != nil {
		idx = int(*n)
	}
	newStr, ok := a.rawString("new_string")
	if !ok {
		return "", fmt.Errorf("EditNotebook requires new_string")
	}
	newStr = normalizeNewlines(newStr)
	isNew, _ := a["is_new_cell"].(bool)
	if isNew {
		if idx < 0 || idx > len(rawCells) {
			return "", fmt.Errorf("cell_idx %d is past the end of the notebook", idx)
		}
		lang, _ := a.str("cell_language")
		cellType := "code"
		if lang == "markdown" || lang == "raw" {
			cellType = lang
		}
		cell := map[string]any{"cell_type": cellType, "metadata": map[string]any{}, "source": sourceLines(newStr)}
		if cellType == "code" {
			cell["execution_count"] = nil
			cell["outputs"] = []any{}
		}
		rawCells = append(rawCells[:idx], append([]any{cell}, rawCells[idx:]...)...)
		notebook["cells"] = rawCells
	} else {
		if idx < 0 || idx >= len(rawCells) {
			return "", fmt.Errorf("cell_idx %d does not exist", idx)
		}
		cell, ok := rawCells[idx].(map[string]any)
		if !ok {
			return "", fmt.Errorf("cell %d is not an object", idx)
		}
		old, ok := a.rawString("old_string")
		if !ok || normalizeNewlines(old) == "" {
			return "", fmt.Errorf("old_string must not be empty")
		}
		old = normalizeNewlines(old)
		source := notebookSource(cell["source"])
		count := strings.Count(source, old)
		if count == 0 {
			return "", fmt.Errorf("old_string was not found in the notebook cell")
		}
		if count > 1 {
			return "", fmt.Errorf("old_string is not unique in the notebook cell; found %d occurrences", count)
		}
		cell["source"] = sourceLines(strings.Replace(source, old, newStr, 1))
	}
	out, err := json.MarshalIndent(notebook, "", " ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

func notebookSource(value any) string {
	switch v := value.(type) {
	case string:
		return normalizeNewlines(v)
	case []any:
		var b strings.Builder
		for _, line := range v {
			b.WriteString(fmt.Sprint(line))
		}
		return normalizeNewlines(b.String())
	default:
		return ""
	}
}

func sourceLines(text string) []any {
	if text == "" {
		return []any{""}
	}
	parts := strings.SplitAfter(text, "\n")
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []any{""}
	}
	return out
}

func fileFromRead(call provider.ToolCall, r *cursorpb.ReadResult) (string, error) {
	switch v := r.GetResult().(type) {
	case *cursorpb.ReadResult_Success:
		if v.Success.GetTruncated() {
			return "", fmt.Errorf("cannot edit a truncated Read result")
		}
		switch out := v.Success.GetOutput().(type) {
		case *cursorpb.ReadSuccess_Content:
			return normalizeNewlines(out.Content), nil
		case *cursorpb.ReadSuccess_Data:
			return "", fmt.Errorf("cannot edit a binary file")
		default:
			return "", fmt.Errorf("Read result has no file content")
		}
	case *cursorpb.ReadResult_FileNotFound:
		if normalize(call.Name) == "write" {
			return "", nil
		}
		return "", fmt.Errorf("file not found")
	case *cursorpb.ReadResult_Error:
		return "", fmt.Errorf("%s", v.Error.GetError())
	case *cursorpb.ReadResult_Rejected:
		return "", fmt.Errorf("%s", v.Rejected.GetReason())
	case *cursorpb.ReadResult_PermissionDenied:
		return "", fmt.Errorf("read permission denied")
	case *cursorpb.ReadResult_InvalidFile:
		return "", fmt.Errorf("%s", v.InvalidFile.GetReason())
	default:
		return "", fmt.Errorf("Read result is empty")
	}
}

func replaceString(a args, before string) (string, error) {
	old, ok := a.rawString("old_string")
	if !ok {
		return "", fmt.Errorf("StrReplace requires old_string")
	}
	next, ok := a.rawString("new_string")
	if !ok {
		return "", fmt.Errorf("StrReplace requires new_string")
	}
	old = normalizeNewlines(old)
	next = normalizeNewlines(next)
	if old == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	count := strings.Count(before, old)
	replaceAll, _ := a["replace_all"].(bool)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string was not found")
	case !replaceAll && count > 1:
		return "", fmt.Errorf("old_string is not unique; found %d occurrences", count)
	case replaceAll:
		return strings.ReplaceAll(before, old, next), nil
	default:
		return strings.Replace(before, old, next, 1), nil
	}
}

func normalizeNewlines(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func failEdit(ui *cursorpb.ToolCall, path, message string) {
	if tool, ok := ui.GetTool().(*cursorpb.ToolCall_EditToolCall); ok {
		tool.EditToolCall.Result = editError(path, message)
	}
}

func editError(path, message string) *cursorpb.EditResult {
	return &cursorpb.EditResult{Result: &cursorpb.EditResult_Error{Error: &cursorpb.EditError{
		Path:              path,
		Error:             message,
		ModelVisibleError: &message,
	}}}
}

func editSuccess(path, before, after string) *cursorpb.EditResult {
	added, removed, diff := unifiedDiff(before, after)
	return &cursorpb.EditResult{Result: &cursorpb.EditResult_Success{Success: &cursorpb.EditSuccess{
		Path:                  path,
		LinesAdded:            &added,
		LinesRemoved:          &removed,
		DiffString:            &diff,
		BeforeFullFileContent: &before,
		AfterFullFileContent:  after,
	}}}
}

func editResultFromWrite(p *Pending, r *cursorpb.WriteResult) *cursorpb.EditResult {
	switch v := r.GetResult().(type) {
	case *cursorpb.WriteResult_Success:
		before, after := "", v.Success.GetFileContentAfterWrite()
		if p.edit != nil {
			before, after = p.edit.before, p.edit.after
		}
		path := v.Success.GetPath()
		if path == "" && p.edit != nil {
			path = p.edit.path
		}
		return editSuccess(path, before, after)
	case *cursorpb.WriteResult_PermissionDenied:
		return &cursorpb.EditResult{Result: &cursorpb.EditResult_WritePermissionDenied{WritePermissionDenied: &cursorpb.EditWritePermissionDenied{
			Path:       v.PermissionDenied.GetPath(),
			Error:      v.PermissionDenied.GetError(),
			IsReadonly: v.PermissionDenied.GetIsReadonly(),
		}}}
	case *cursorpb.WriteResult_NoSpace:
		return editError(v.NoSpace.GetPath(), "no space left")
	case *cursorpb.WriteResult_Error:
		return editError(v.Error.GetPath(), v.Error.GetError())
	case *cursorpb.WriteResult_Rejected:
		return &cursorpb.EditResult{Result: &cursorpb.EditResult_Rejected{Rejected: &cursorpb.EditRejected{
			Path:   v.Rejected.GetPath(),
			Reason: v.Rejected.GetReason(),
		}}}
	default:
		path := ""
		if p.edit != nil {
			path = p.edit.path
		}
		return editError(path, "Cursor returned an empty write result")
	}
}

func writeText(r *cursorpb.WriteResult) (string, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.WriteResult_Success:
		if content := v.Success.GetFileContentAfterWrite(); content != "" {
			return content, false
		}
		return fmt.Sprintf("write success path=%s lines=%d", v.Success.GetPath(), v.Success.GetLinesCreated()), false
	case *cursorpb.WriteResult_PermissionDenied:
		return v.PermissionDenied.GetError(), true
	case *cursorpb.WriteResult_NoSpace:
		return "no space left: " + v.NoSpace.GetPath(), true
	case *cursorpb.WriteResult_Error:
		return v.Error.GetError(), true
	case *cursorpb.WriteResult_Rejected:
		return v.Rejected.GetReason(), true
	default:
		return "Cursor returned an empty write result", true
	}
}

func deleteText(r *cursorpb.DeleteResult) (string, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.DeleteResult_Success:
		return "delete success path=" + v.Success.GetPath(), false
	case *cursorpb.DeleteResult_FileNotFound:
		return "file not found: " + v.FileNotFound.GetPath(), true
	case *cursorpb.DeleteResult_NotFile:
		return "not file: " + v.NotFile.GetPath(), true
	case *cursorpb.DeleteResult_PermissionDenied:
		return v.PermissionDenied.GetClientVisibleError(), true
	case *cursorpb.DeleteResult_FileBusy:
		return "file busy: " + v.FileBusy.GetPath(), true
	case *cursorpb.DeleteResult_Rejected:
		return v.Rejected.GetReason(), true
	case *cursorpb.DeleteResult_Error:
		return v.Error.GetError(), true
	default:
		return "Cursor returned an empty delete result", true
	}
}

func unifiedDiff(before, after string) (int32, int32, string) {
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:       difflib.SplitLines(before),
		B:       difflib.SplitLines(after),
		Context: 3,
		Eol:     "\n",
	})
	if err != nil || text == "" {
		return 0, 0, ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	start := 0
	for start < len(lines) && !strings.HasPrefix(lines[start], "@@") {
		start++
	}
	var body strings.Builder
	var added, removed int32
	for _, line := range lines[start:] {
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	return added, removed, body.String()
}
