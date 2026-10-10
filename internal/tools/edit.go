package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	case "applypatch":
		text, _ := a.rawString("patch")
		stream = normalizeNewlines(text)
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
	case "applypatch":
		text, _ := a.rawString("patch")
		ops, perr := parsePatch(text)
		if perr == nil {
			after, perr = applyPatchHunks(before, ops[0].hunks)
		}
		if perr != nil {
			failEdit(ui, p.edit.path, perr.Error())
			return nil, perr.Error(), true
		}
	case "write":
		contents, _ := a.rawString("contents", "content")
		after = restoreNewlines(normalizeNewlines(contents), usesCRLF(before))
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
	crlf := usesCRLF(before)
	old = normalizeNewlines(old)
	next = normalizeNewlines(next)
	before = normalizeNewlines(before)
	if old == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	replaceAll, _ := a["replace_all"].(bool)
	after, err := replaceNormalized(before, old, next, replaceAll)
	if err != nil {
		return "", err
	}
	return restoreNewlines(after, crlf), nil
}

// replaceNormalized 依次尝试：精确 → 忽略空白 → 修正常见笔误（行号前缀、尾部多余换行、弯引号）
// 后再精确/忽略空白 → 唯一高相似度块。只有「找不到」才继续往下试；「不唯一」立即报错，不去猜。
func replaceNormalized(before, old, next string, replaceAll bool) (string, error) {
	notFound := fmt.Errorf("old_string was not found")
	try := func(old, next string) (string, error) {
		count := strings.Count(before, old)
		switch {
		case count == 1 || (replaceAll && count > 0):
			if replaceAll {
				return strings.ReplaceAll(before, old, next), nil
			}
			return strings.Replace(before, old, next, 1), nil
		case !replaceAll && count > 1:
			return "", fmt.Errorf("old_string is not unique; found %d occurrences", count)
		}
		return whitespaceReplace(before, old, next, replaceAll)
	}
	variants := [][2]string{{old, next}}
	for _, fix := range []func(string, string) (string, string, bool){stripLineNumbers, trimExtraNewlines, straightQuotes} {
		base := variants[len(variants)-1]
		if o, n, ok := fix(base[0], base[1]); ok {
			variants = append(variants, [2]string{o, n})
		}
		if o, n, ok := fix(old, next); ok && (o != base[0] || n != base[1]) {
			variants = append(variants, [2]string{o, n})
		}
	}
	for _, v := range variants {
		after, err := try(v[0], v[1])
		if err == nil {
			return after, nil
		}
		if !strings.Contains(err.Error(), "not found") {
			return "", err
		}
	}
	if replaceAll {
		return "", notFound
	}
	for _, v := range variants {
		if after, ok := fuzzyReplace(before, v[0], v[1]); ok {
			return after, nil
		}
	}
	return "", notFound
}

func usesCRLF(text string) bool {
	crlf := strings.Count(text, "\r\n")
	return crlf > 0 && crlf*2 >= strings.Count(text, "\n")
}

// restoreNewlines 把结果换回原文件的 CRLF，免得一次小改把整个 Windows 文件改成 LF。
func restoreNewlines(text string, crlf bool) string {
	if !crlf {
		return text
	}
	return strings.ReplaceAll(text, "\n", "\r\n")
}

// reLineNumber 匹配 Read 输出里的行号前缀："     12|"、"12\t"、"L12:"、"12→"。
var reLineNumber = regexp.MustCompile(`^(?:\s*\d+(?:\||\t|→)|L\d+:)`)

// stripLineNumbers：模型把 Read 结果的行号前缀一起抄进了 old_string。只有每一行都带前缀才剥。
func stripLineNumbers(old, next string) (string, string, bool) {
	strip := func(text string) (string, bool) {
		lines := strings.Split(text, "\n")
		seen := 0
		for i, line := range lines {
			if line == "" {
				continue
			}
			loc := reLineNumber.FindStringIndex(line)
			if loc == nil {
				return text, false
			}
			lines[i] = line[loc[1]:]
			seen++
		}
		return strings.Join(lines, "\n"), seen > 0
	}
	o, ok := strip(old)
	if !ok {
		return old, next, false
	}
	if n, ok := strip(next); ok {
		next = n
	}
	return o, next, true
}

// trimExtraNewlines：old_string 末尾多抄了空行。new_string 去掉同样多的尾部换行。
func trimExtraNewlines(old, next string) (string, string, bool) {
	o := strings.TrimRight(old, "\n")
	if o == old || o == "" {
		return old, next, false
	}
	drop := len(old) - len(o)
	n := next
	for i := 0; i < drop && strings.HasSuffix(n, "\n"); i++ {
		n = n[:len(n)-1]
	}
	return o, n, true
}

var quoteFixer = strings.NewReplacer("“", `"`, "”", `"`, "„", `"`, "‘", "'", "’", "'", "‚", "'")

// straightQuotes：模型把直引号写成了弯引号。
func straightQuotes(old, next string) (string, string, bool) {
	o := quoteFixer.Replace(old)
	if o == old {
		return old, next, false
	}
	return o, quoteFixer.Replace(next), true
}

// whitespaceReplace 忽略行首缩进与行内连续空白差异。
func whitespaceReplace(before, old, next string, replaceAll bool) (string, error) {
	matches := findWhitespaceMatches(before, old)
	switch {
	case len(matches) == 0:
		return "", fmt.Errorf("old_string was not found")
	case !replaceAll && len(matches) > 1:
		return "", fmt.Errorf("old_string is not unique; found %d occurrences", len(matches))
	case replaceAll:
		out := before
		for i := len(matches) - 1; i >= 0; i-- {
			m := matches[i]
			out = out[:m.start] + alignIndent(next, old, m) + out[m.end:]
		}
		return out, nil
	default:
		m := matches[0]
		return before[:m.start] + alignIndent(next, old, m) + before[m.end:], nil
	}
}

type span struct {
	start, end int
	indent     string
	lines      []string
}

func findWhitespaceMatches(before, old string) []span {
	oldLines := strings.Split(old, "\n")
	fileLines := strings.Split(before, "\n")
	if len(oldLines) == 0 || len(fileLines) < len(oldLines) {
		return nil
	}
	var out []span
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		if !wsLinesEqual(fileLines[i:i+len(oldLines)], oldLines) {
			continue
		}
		start := lineOffset(fileLines, i)
		end := start
		for j := 0; j < len(oldLines); j++ {
			end += len(fileLines[i+j])
			if j+1 < len(oldLines) {
				end++
			}
		}
		out = append(out, span{start: start, end: end, indent: leadingIndent(fileLines[i]), lines: fileLines[i : i+len(oldLines)]})
	}
	return out
}

func wsLinesEqual(file, old []string) bool {
	if len(file) != len(old) {
		return false
	}
	for i := range old {
		if collapseWS(strings.TrimSpace(file[i])) != collapseWS(strings.TrimSpace(old[i])) {
			return false
		}
	}
	return true
}

func collapseWS(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func leadingIndent(line string) string {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[:i]
}

// alignIndent 把 new_string 的缩进换成文件里的写法：首行对齐匹配处，
// 其余行保留相对缩进层级，并在 tab 与空格之间按各自的缩进单位换算。
func alignIndent(next, old string, m span) string {
	oldLines := strings.Split(old, "\n")
	oldBase := leadingIndent(oldLines[0])
	fileTabs := strings.Contains(m.indent, "\t")
	if !fileTabs {
		for _, l := range m.lines {
			if strings.HasPrefix(leadingIndent(l), "\t") {
				fileTabs = true
				break
			}
		}
	}
	oldUnit := indentUnit(oldLines, oldBase)
	fileUnit := indentUnit(m.lines, m.indent)
	lines := strings.Split(next, "\n")
	for i, line := range lines {
		lead := leadingIndent(line)
		body := line[len(lead):]
		if body == "" {
			lines[i] = ""
			continue
		}
		rel := lead
		if strings.HasPrefix(lead, oldBase) {
			rel = lead[len(oldBase):]
		} else {
			rel = ""
		}
		levels := indentLevels(rel, oldUnit)
		if fileTabs {
			lines[i] = m.indent + strings.Repeat("\t", levels) + body
		} else {
			lines[i] = m.indent + strings.Repeat(" ", levels*fileUnit) + body
		}
	}
	return strings.Join(lines, "\n")
}

// indentUnit 估计一段文本相对 base 的空格缩进单位（最小的非零相对缩进），默认 4。
func indentUnit(lines []string, base string) int {
	unit := 0
	for _, l := range lines {
		lead := leadingIndent(l)
		if strings.TrimSpace(l) == "" || !strings.HasPrefix(lead, base) {
			continue
		}
		rel := lead[len(base):]
		if rel == "" || strings.Contains(rel, "\t") {
			continue
		}
		if unit == 0 || len(rel) < unit {
			unit = len(rel)
		}
	}
	if unit == 0 {
		return 4
	}
	return unit
}

// indentLevels 把相对缩进折算成层数：tab 一层，unit 个空格一层。
func indentLevels(rel string, unit int) int {
	levels, spaces := 0, 0
	for _, r := range rel {
		if r == '\t' {
			levels++
			continue
		}
		spaces++
	}
	if unit <= 0 {
		unit = 4
	}
	return levels + (spaces+unit-1)/unit
}

func lineOffset(lines []string, idx int) int {
	n := 0
	for i := 0; i < idx && i < len(lines); i++ {
		n += len(lines[i]) + 1
	}
	return n
}

// fuzzyReplace 在唯一高相似度行块上替换。行级不够时再比字符级，阈值 0.85。
func fuzzyReplace(before, old, next string) (string, bool) {
	oldLines := strings.Split(old, "\n")
	fileLines := strings.Split(before, "\n")
	if len(oldLines) == 0 || len(fileLines) < len(oldLines) {
		return "", false
	}
	bestStart := -1
	bestScore := 0.0
	ties := 0
	n := len(oldLines)
	joinedOld := strings.Join(oldLines, "\n")
	lm := difflib.NewMatcher(nil, oldLines)
	cr := newCharRatio(joinedOld)
	for i := 0; i+n <= len(fileLines); i++ {
		window := fileLines[i : i+n]
		lm.SetSeq1(window)
		score := lm.Ratio()
		if score < 0.85 {
			if char := cr.ratio(strings.Join(window, "\n")); char > score {
				score = char
			}
		}
		if score > bestScore+0.001 {
			bestScore = score
			bestStart = i
			ties = 1
		} else if absFloat(score-bestScore) <= 0.001 && score >= 0.85 {
			ties++
		}
	}
	if bestScore < 0.85 || ties != 1 || bestStart < 0 {
		return "", false
	}
	prefix := strings.Join(fileLines[:bestStart], "\n")
	suffix := strings.Join(fileLines[bestStart+n:], "\n")
	var b strings.Builder
	if bestStart > 0 {
		b.WriteString(prefix)
		b.WriteByte('\n')
	}
	b.WriteString(next)
	if bestStart+n < len(fileLines) {
		b.WriteByte('\n')
		b.WriteString(suffix)
	}
	return b.String(), true
}

func textRatio(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) > 4000 || len(b) > 4000 {
		return difflib.NewMatcher(difflib.SplitLines(a), difflib.SplitLines(b)).Ratio()
	}
	as := make([]string, 0, len(a))
	bs := make([]string, 0, len(b))
	for _, r := range a {
		as = append(as, string(r))
	}
	for _, r := range b {
		bs = append(bs, string(r))
	}
	m := difflib.NewMatcher(as, bs)
	// QuickRatio 是 Ratio 的上界且是线性的；大文件里绝大多数窗口在这里就被排除，
	// 免得逐窗口做字符级 diff 拖到几十秒。
	if m.RealQuickRatio() < 0.85 || m.QuickRatio() < 0.85 {
		return m.QuickRatio()
	}
	return m.Ratio()
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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
