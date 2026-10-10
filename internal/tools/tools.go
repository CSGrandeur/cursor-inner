// Package tools 在模型的工具调用和 Cursor 的执行协议之间转换。
// 工具定义取自 cursor-byok 的 prompt/cursor/tools.json。
package tools

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

//go:embed tools.json
var catalogJSON []byte

func Catalog() []provider.Tool {
	var tools []provider.Tool
	if err := json.Unmarshal(catalogJSON, &tools); err != nil {
		panic(err)
	}
	return tools
}

// Pending 是已经下发给 Cursor、等待执行结果的工具调用。
// edit 非空时，先读文件再写入；Advance 在读完后返回下一条执行请求。
// Shell 用同一个 id 连续返回输出，直到结束、拒绝或转入后台。
type Pending struct {
	Call      provider.ToolCall
	edit      *editState
	shell     *shellState
	Terminals string
	// Note 是交给模型的补充说明，例如云端子代理改在本地执行。
	Note string
	// Images 是这次工具结果里要交给模型看的图片。读到 PNG、JPEG、GIF、WebP 时填上。
	Images []provider.Image
	// Wait 是这条执行在没有新消息时最多再等多久。为零时由调用方用自己的默认值。
	Wait time.Duration
}

// Request 把模型的工具调用变成 Cursor 的执行请求和界面卡片。返回错误时，应把错误文本作为工具结果交回模型。
func Request(id uint32, call provider.ToolCall) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("arguments of %s are not valid JSON: %w", call.Name, err)
	}
	exec := &cursorpb.ExecServerMessage{Id: id, ExecId: fmt.Sprintf("%s-%d", call.ID, id)}
	ui := &cursorpb.ToolCall{ToolCallId: &call.ID}
	switch normalize(call.Name) {
	case "read":
		path, ok := a.str("path", "file_path", "filePath")
		if !ok {
			return nil, nil, nil, fmt.Errorf("Read requires path")
		}
		read := &cursorpb.ReadArgs{Path: path, ToolCallId: call.ID, Offset: a.int32("offset")}
		if limit := a.int32("limit"); limit != nil && *limit > 0 {
			v := uint32(*limit)
			read.Limit = &v
		}
		exec.Message = &cursorpb.ExecServerMessage_ReadArgs{ReadArgs: read}
		ui.Tool = &cursorpb.ToolCall_ReadToolCall{ReadToolCall: &cursorpb.ReadToolCall{Args: &cursorpb.ReadToolArgs{Path: path, Offset: read.Offset, Limit: a.int32("limit")}}}
	case "grep":
		pattern, ok := a.str("pattern")
		if !ok {
			return nil, nil, nil, fmt.Errorf("Grep requires pattern")
		}
		grep := &cursorpb.GrepArgs{
			Pattern:         pattern,
			Path:            a.optStr("path"),
			Glob:            a.optStr("glob"),
			OutputMode:      a.optStr("output_mode"),
			ContextBefore:   a.int32("-B"),
			ContextAfter:    a.int32("-A"),
			Context:         a.int32("-C"),
			CaseInsensitive: a.bool("-i"),
			Type:            a.optStr("type"),
			HeadLimit:       a.int32("head_limit"),
			Multiline:       a.bool("multiline"),
			Offset:          a.int32("offset"),
			ToolCallId:      call.ID,
		}
		exec.Message = &cursorpb.ExecServerMessage_GrepArgs{GrepArgs: grep}
		ui.Tool = &cursorpb.ToolCall_GrepToolCall{GrepToolCall: &cursorpb.GrepToolCall{Args: grep}}
	case "glob":
		pattern, ok := a.str("glob_pattern", "pattern")
		if !ok {
			return nil, nil, nil, fmt.Errorf("Glob requires glob_pattern")
		}
		dir := a.optStr("target_directory")
		mode := "files_with_matches"
		exec.Message = &cursorpb.ExecServerMessage_GrepArgs{GrepArgs: &cursorpb.GrepArgs{Path: dir, Glob: &pattern, OutputMode: &mode, ToolCallId: call.ID}}
		ui.Tool = &cursorpb.ToolCall_GlobToolCall{GlobToolCall: &cursorpb.GlobToolCall{Args: &cursorpb.GlobToolArgs{TargetDirectory: dir, GlobPattern: pattern}}}
	case "delete":
		path, ok := a.str("path", "file_path", "filePath")
		if !ok {
			return nil, nil, nil, fmt.Errorf("Delete requires path")
		}
		del := &cursorpb.DeleteArgs{Path: path, ToolCallId: call.ID}
		exec.Message = &cursorpb.ExecServerMessage_DeleteArgs{DeleteArgs: del}
		ui.Tool = &cursorpb.ToolCall_DeleteToolCall{DeleteToolCall: &cursorpb.DeleteToolCall{Args: del}}
	case "strreplace", "write", "editnotebook":
		return startEdit(id, call, a)
	case "shell", "bash":
		if command, ok := a.str("command"); ok {
			if text, ok := shellPatch(command); ok {
				rewritten, err := patchCall(call, text)
				if err != nil {
					return nil, nil, nil, err
				}
				return Request(id, rewritten)
			}
		}
		return startShell(id, call, a)
	case "applypatch":
		if _, ok := a.rawString("patch"); ok {
			if _, ok := a.str("path"); ok {
				return startEdit(id, call, a)
			}
		}
		text, ok := a.rawString("patch", "input", "diff")
		if !ok {
			return nil, nil, nil, fmt.Errorf("apply_patch requires the patch text in input")
		}
		rewritten, err := patchCall(call, text)
		if err != nil {
			return nil, nil, nil, err
		}
		return Request(id, rewritten)
	case "readlints":
		return startLints(id, call, a)
	case "task":
		return startTask(id, call, a)
	case "fetchmcpresource":
		return startFetchResource(id, call, a)
	default:
		return nil, nil, nil, fmt.Errorf("tool %s is not available", call.Name)
	}
	return exec, ui, &Pending{Call: call}, nil
}

// Advance 消化一条执行结果。需要继续执行时返回下一条请求（调用方填 id）；否则返回交给模型的文本。
func (p *Pending) Advance(msg *cursorpb.ExecClientMessage, ui *cursorpb.ToolCall) (*cursorpb.ExecServerMessage, string, bool) {
	if p.edit != nil && p.edit.phase == editRead {
		return p.afterRead(msg, ui)
	}
	text, isErr := p.Result(msg, ui)
	return nil, text, isErr
}

// Result 把 Cursor 的执行结果变成交给模型的文本，并补全界面卡片里的结果。
func (p *Pending) Result(msg *cursorpb.ExecClientMessage, ui *cursorpb.ToolCall) (string, bool) {
	p.Images = nil
	switch result := msg.GetMessage().(type) {
	case *cursorpb.ExecClientMessage_ReadResult:
		text, images, isErr := readText(result.ReadResult)
		p.Images = images
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_ReadToolCall); ok {
			tool.ReadToolCall.Result = readUI(result.ReadResult)
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_GrepResult:
		text, isErr := grepText(result.GrepResult)
		switch tool := ui.GetTool().(type) {
		case *cursorpb.ToolCall_GrepToolCall:
			tool.GrepToolCall.Result = result.GrepResult
		case *cursorpb.ToolCall_GlobToolCall:
			tool.GlobToolCall.Result = globUI(result.GrepResult)
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_WriteResult:
		text, isErr := writeText(result.WriteResult)
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_EditToolCall); ok {
			tool.EditToolCall.Result = editResultFromWrite(p, result.WriteResult)
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_DiagnosticsResult:
		text, isErr := lintText(result.DiagnosticsResult)
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_ReadLintsToolCall); ok {
			tool.ReadLintsToolCall.Result = lintUI(result.DiagnosticsResult, text, isErr)
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_DeleteResult:
		text, isErr := deleteText(result.DeleteResult)
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_DeleteToolCall); ok {
			tool.DeleteToolCall.Result = result.DeleteResult
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_McpResult:
		text, images, isErr := mcpText(result.McpResult)
		p.Images = images
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_McpToolCall); ok {
			tool.McpToolCall.Result = mcpUI(result.McpResult)
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_SubagentResult:
		text, isErr := subagentText(result.SubagentResult)
		if p.Note != "" {
			text = p.Note + "\n" + text
		}
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_TaskToolCall); ok {
			tool.TaskToolCall.Result = taskUI(result.SubagentResult)
		}
		return text, isErr
	case *cursorpb.ExecClientMessage_ReadMcpResourceExecResult:
		text, images, isErr := resourceText(result.ReadMcpResourceExecResult)
		p.Images = images
		if tool, ok := ui.GetTool().(*cursorpb.ToolCall_ReadMcpResourceToolCall); ok {
			tool.ReadMcpResourceToolCall.Result = result.ReadMcpResourceExecResult
		}
		return text, isErr
	default:
		return fmt.Sprintf("Cursor returned an unexpected result for %s", p.Call.Name), true
	}
}

func readText(r *cursorpb.ReadResult) (string, []provider.Image, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.ReadResult_Success:
		switch out := v.Success.GetOutput().(type) {
		case *cursorpb.ReadSuccess_Content:
			if out.Content == "" {
				return "File is empty.", nil, false
			}
			return out.Content, nil, false
		case *cursorpb.ReadSuccess_Data:
			if mime := imageMIME(out.Data); mime != "" {
				return "Read image file: " + v.Success.GetPath(), []provider.Image{{MIME: mime, Data: append([]byte(nil), out.Data...)}}, false
			}
			return fmt.Sprintf("%s is a binary file (%d bytes).", v.Success.GetPath(), len(out.Data)), nil, false
		}
		return "read " + v.Success.GetPath(), nil, false
	case *cursorpb.ReadResult_Error:
		return v.Error.GetError(), nil, true
	case *cursorpb.ReadResult_Rejected:
		return "The user rejected reading " + v.Rejected.GetPath() + ": " + v.Rejected.GetReason(), nil, true
	case *cursorpb.ReadResult_FileNotFound:
		return "File not found: " + v.FileNotFound.GetPath(), nil, true
	case *cursorpb.ReadResult_PermissionDenied:
		return "Permission denied: " + v.PermissionDenied.GetPath(), nil, true
	case *cursorpb.ReadResult_InvalidFile:
		return v.InvalidFile.GetReason(), nil, true
	}
	return "Cursor returned an empty read result", nil, true
}

func readUI(r *cursorpb.ReadResult) *cursorpb.ReadToolResult {
	success, ok := r.GetResult().(*cursorpb.ReadResult_Success)
	if !ok {
		text, _, _ := readText(r)
		return &cursorpb.ReadToolResult{Result: &cursorpb.ReadToolResult_Error{Error: &cursorpb.ReadToolError{ErrorMessage: text}}}
	}
	s := success.Success
	out := &cursorpb.ReadToolSuccess{Path: s.GetPath(), TotalLines: uint32(max(s.GetTotalLines(), 0)), FileSize: uint32(max(s.GetFileSize(), 0)), ExceededLimit: s.GetTruncated()}
	switch o := s.GetOutput().(type) {
	case *cursorpb.ReadSuccess_Content:
		out.IsEmpty = o.Content == ""
		out.Output = &cursorpb.ReadToolSuccess_Content{Content: o.Content}
	case *cursorpb.ReadSuccess_Data:
		out.Output = &cursorpb.ReadToolSuccess_Data{Data: o.Data}
	}
	return &cursorpb.ReadToolResult{Result: &cursorpb.ReadToolResult_Success{Success: out}}
}

func grepText(r *cursorpb.GrepResult) (string, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.GrepResult_Error:
		return v.Error.GetError(), true
	case *cursorpb.GrepResult_Success:
		var lines []string
		if active := v.Success.GetActiveEditorResult(); active != nil {
			lines = appendUnion(lines, active)
		}
		names := make([]string, 0, len(v.Success.GetWorkspaceResults()))
		for name := range v.Success.GetWorkspaceResults() {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			lines = appendUnion(lines, v.Success.GetWorkspaceResults()[name])
		}
		if len(lines) == 0 {
			return fmt.Sprintf("No matches found for pattern `%s` in %s", v.Success.GetPattern(), v.Success.GetPath()), false
		}
		return strings.Join(lines, "\n"), false
	}
	return "Cursor returned an empty search result", true
}

func appendUnion(lines []string, u *cursorpb.GrepUnionResult) []string {
	truncated := func(client, ripgrep bool, total int32, unit string) {
		if client || ripgrep {
			lines = append(lines, fmt.Sprintf("[Results truncated; %d total %s]", total, unit))
		}
	}
	switch r := u.GetResult().(type) {
	case *cursorpb.GrepUnionResult_Files:
		lines = append(lines, r.Files.GetFiles()...)
		truncated(r.Files.GetClientTruncated(), r.Files.GetRipgrepTruncated(), r.Files.GetTotalFiles(), "files")
	case *cursorpb.GrepUnionResult_Count:
		for _, c := range r.Count.GetCounts() {
			lines = append(lines, fmt.Sprintf("%s:%d", c.GetFile(), c.GetCount()))
		}
		truncated(r.Count.GetClientTruncated(), r.Count.GetRipgrepTruncated(), r.Count.GetTotalMatches(), "matches")
	case *cursorpb.GrepUnionResult_Content:
		for _, file := range r.Content.GetMatches() {
			for _, m := range file.GetMatches() {
				sep := ":"
				if m.GetIsContextLine() {
					sep = "-"
				}
				line := fmt.Sprintf("%s%s%d%s%s", file.GetFile(), sep, m.GetLineNumber(), sep, m.GetContent())
				if m.GetContentTruncated() {
					line += " [line truncated]"
				}
				lines = append(lines, line)
			}
		}
		truncated(r.Content.GetClientTruncated(), r.Content.GetRipgrepTruncated(), r.Content.GetTotalMatchedLines(), "matched lines")
	}
	return lines
}

func globUI(r *cursorpb.GrepResult) *cursorpb.GlobToolResult {
	switch v := r.GetResult().(type) {
	case *cursorpb.GrepResult_Error:
		return &cursorpb.GlobToolResult{Result: &cursorpb.GlobToolResult_Error{Error: &cursorpb.GlobToolError{Error: v.Error.GetError()}}}
	case *cursorpb.GrepResult_Success:
		out := &cursorpb.GlobToolSuccess{Pattern: v.Success.GetPattern(), Path: v.Success.GetPath()}
		for _, u := range v.Success.GetWorkspaceResults() {
			if files, ok := u.GetResult().(*cursorpb.GrepUnionResult_Files); ok {
				out.Files = append(out.Files, files.Files.GetFiles()...)
				out.TotalFiles += files.Files.GetTotalFiles()
				out.ClientTruncated = out.ClientTruncated || files.Files.GetClientTruncated()
				out.RipgrepTruncated = out.RipgrepTruncated || files.Files.GetRipgrepTruncated()
			}
		}
		return &cursorpb.GlobToolResult{Result: &cursorpb.GlobToolResult_Success{Success: out}}
	}
	return nil
}

func normalize(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

// ReadOnly 是可以和其他只读调用并行的工具。
func ReadOnly(name string) bool {
	switch normalize(name) {
	case "read", "grep", "glob":
		return true
	default:
		return false
	}
}

const modelTextLimit = 32 * 1024

// ClipForModel 把交给模型的工具结果限制在大约 32KB，界面卡片仍用原文。
func ClipForModel(text string) string {
	if len(text) <= modelTextLimit {
		return text
	}
	const head, tail = 24 * 1024, 4 * 1024
	note := "\n\n[... truncated; use offset and limit to read the rest ...]\n\n"
	return text[:head] + note + text[len(text)-tail:]
}

// SchemaHint 在参数错误时附上该工具的参数定义。
func SchemaHint(name string) string {
	for _, tool := range Catalog() {
		if strings.EqualFold(tool.Name, name) {
			return "\nparameters: " + string(tool.Parameters)
		}
	}
	return ""
}

type args map[string]any

func parseArgs(raw string) (args, error) {
	a := args{}
	if strings.TrimSpace(raw) == "" {
		return a, nil
	}
	return a, json.Unmarshal([]byte(raw), &a)
}

func (a args) str(names ...string) (string, bool) {
	for _, name := range names {
		if v, ok := a[name].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// rawString 区分「字段缺失」和「值为空字符串」。
func (a args) rawString(names ...string) (string, bool) {
	for _, name := range names {
		if v, ok := a[name].(string); ok {
			return v, true
		}
	}
	return "", false
}

func (a args) optStr(name string) *string {
	if v, ok := a.str(name); ok {
		return &v
	}
	return nil
}

func (a args) int32(name string) *int32 {
	switch v := a[name].(type) {
	case float64:
		n := int32(v)
		return &n
	case string:
		var n int32
		if _, err := fmt.Sscan(v, &n); err == nil {
			return &n
		}
	}
	return nil
}

func (a args) bool(name string) *bool {
	if v, ok := a[name].(bool); ok {
		return &v
	}
	return nil
}
