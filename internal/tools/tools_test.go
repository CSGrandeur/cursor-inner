package tools

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

func TestMCPDefsIncludesDescriptorTools(t *testing.T) {
	schema := `{"type":"object","properties":{}}`
	ctx := &cursorpb.RequestContext{
		McpMetaToolOptions: &cursorpb.McpMetaToolOptions{Enabled: true, McpDescriptors: []*cursorpb.McpDescriptor{{
			ServerIdentifier: "project-0-workspace-probe",
			ServerName:       "probe",
			Tools: []*cursorpb.McpToolDescriptor{{
				ToolName:        "probe_ping",
				Description:     protoString("Return probe-pong."),
				InputSchemaJson: &schema,
			}},
		}}},
	}
	defs := MCPDefs(ctx)
	if len(defs) != 1 || defs[0].GetName() != "project-0-workspace-probe-probe_ping" || defs[0].GetProviderIdentifier() != "probe" {
		t.Fatalf("%+v", defs)
	}
	got := MCPCatalog(defs)
	if len(got) != 1 || got[0].Name != "probe_ping" || !strings.Contains(got[0].Description, "probe-pong") {
		t.Fatalf("%+v", got)
	}
	exec, _, _, err := MCPExec(1, provider.ToolCall{ID: "c", Name: "probe_ping", Arguments: `{}`}, defs[0])
	if err != nil || exec.GetMcpArgs().GetName() != "project-0-workspace-probe-probe_ping" || exec.GetMcpArgs().GetProviderIdentifier() != "probe" || exec.GetMcpArgs().GetServerIdentifier() != "project-0-workspace-probe" {
		t.Fatalf("%v %+v", err, exec.GetMcpArgs())
	}
}

func protoString(s string) *string { return &s }

func TestParseGrepLinesKeepsWindowsPaths(t *testing.T) {
	text := "C:\\w\\a.go:12:func decodeBingU\nmy-file.go-3-context\ninternal/tools/websearch.go:66:func decodeBingU"
	got := ParseGrepLines(text, true)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].Path != `C:\w\a.go` || got[0].Line != "func decodeBingU" {
		t.Fatalf("%+v", got[0])
	}
	if got[1].Path != "my-file.go" || got[1].Line != "context" {
		t.Fatalf("%+v", got[1])
	}
	if got[2].Path != "internal/tools/websearch.go" || !strings.Contains(got[2].Line, "decodeBingU") {
		t.Fatalf("%+v", got[2])
	}
}

func TestCatalogHasLayerOneTools(t *testing.T) {
	var names []string
	for _, tool := range Catalog() {
		names = append(names, tool.Name)
		if len(tool.Parameters) == 0 || tool.Description == "" {
			t.Fatalf("%s lacks schema", tool.Name)
		}
	}
	if strings.Join(names, ",") != "Glob,Grep,Read,Delete,StrReplace,Write,Shell,ReadLints,TodoWrite,EditNotebook,AskQuestion,SwitchMode,CreatePlan,UpdateCurrentStep,WebSearch,WebFetch,GetMcpTools,CallMcpTool,FetchMcpResource,Task,GenerateImage,SemSearch" {
		t.Fatal(names)
	}
}

func TestReadImageReachesTheModel(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	_, ui, pending, err := Request(1, provider.ToolCall{ID: "c1", Name: "Read", Arguments: `{"path":"/w/E01-D01.png"}`})
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/E01-D01.png", Output: &cursorpb.ReadSuccess_Data{Data: buf.Bytes()}}},
	}}}, ui)
	if isErr || text != "Read image file: /w/E01-D01.png" || len(pending.Images) != 1 || pending.Images[0].MIME != "image/png" || !bytes.Equal(pending.Images[0].Data, buf.Bytes()) {
		t.Fatalf("%q %v %+v", text, isErr, pending.Images)
	}
	text, isErr = pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.bin", Output: &cursorpb.ReadSuccess_Data{Data: []byte{0, 1, 2, 3}}}},
	}}}, ui)
	if isErr || text != "/w/a.bin is a binary file (4 bytes)." || pending.Images != nil {
		t.Fatalf("%q %v", text, pending.Images)
	}
	webp := []byte("RIFF????WEBPVP8X")
	webp = append(webp, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	// RIFF size is the file size minus 8.
	webp[4], webp[5], webp[6], webp[7] = 22, 0, 0, 0
	if imageMIME(webp) != "image/webp" {
		t.Fatal(imageMIME(webp))
	}
}

func TestMCPAndResourceImagesReachTheModel(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	pngBytes := buf.Bytes()
	text, images, isErr := mcpText(&cursorpb.McpResult{Result: &cursorpb.McpResult_Success{Success: &cursorpb.McpSuccess{
		Content: []*cursorpb.McpToolResultContentItem{{
			Content: &cursorpb.McpToolResultContentItem_Image{Image: &cursorpb.McpImageContent{Data: pngBytes, MimeType: "image/png"}},
		}},
	}}})
	if isErr || text != "MCP image: image/png" || len(images) != 1 || !bytes.Equal(images[0].Data, pngBytes) {
		t.Fatalf("%q %v %+v", text, isErr, images)
	}
	text, images, isErr = resourceText(&cursorpb.ReadMcpResourceExecResult{Result: &cursorpb.ReadMcpResourceExecResult_Success{Success: &cursorpb.ReadMcpResourceSuccess{
		Uri:     "file://map.png",
		Content: &cursorpb.ReadMcpResourceSuccess_Blob{Blob: pngBytes},
	}}})
	if isErr || text != "Read image file: file://map.png" || len(images) != 1 || images[0].MIME != "image/png" {
		t.Fatalf("%q %v %+v", text, isErr, images)
	}
	ui := &cursorpb.ToolCall{Tool: &cursorpb.ToolCall_GenerateImageToolCall{GenerateImageToolCall: &cursorpb.GenerateImageToolCall{
		Result: &cursorpb.GenerateImageResult{Result: &cursorpb.GenerateImageResult_Success{Success: &cursorpb.GenerateImageSuccess{
			ImageData: base64.StdEncoding.EncodeToString(pngBytes),
		}}},
	}}}
	got, ok := GeneratedImage(ui)
	if !ok || got.MIME != "image/png" || !bytes.Equal(got.Data, pngBytes) {
		t.Fatalf("%v %+v", ok, got)
	}
}

func TestReadRoundTrip(t *testing.T) {
	exec, ui, pending, err := Request(3, provider.ToolCall{ID: "c1", Name: "Read", Arguments: `{"file_path":"/w/a.go","offset":10,"limit":5}`})
	if err != nil {
		t.Fatal(err)
	}
	args := exec.GetReadArgs()
	if exec.GetId() != 3 || args.GetPath() != "/w/a.go" || args.GetOffset() != 10 || args.GetLimit() != 5 || args.GetToolCallId() != "c1" {
		t.Fatalf("%v", exec)
	}
	if ui.GetReadToolCall().GetArgs().GetPath() != "/w/a.go" || ui.GetToolCallId() != "c1" {
		t.Fatalf("%v", ui)
	}
	text, isErr := pending.Result(&cursorpb.ExecClientMessage{Id: 3, Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", TotalLines: 2, Output: &cursorpb.ReadSuccess_Content{Content: "package a\n"}}},
	}}}, ui)
	if isErr || text != "package a\n" || ui.GetReadToolCall().GetResult().GetSuccess().GetContent() != "package a\n" {
		t.Fatalf("%q %v %v", text, isErr, ui)
	}
	text, isErr = pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_FileNotFound{FileNotFound: &cursorpb.ReadFileNotFound{Path: "/w/x"}},
	}}}, ui)
	if !isErr || text != "File not found: /w/x" || ui.GetReadToolCall().GetResult().GetError().GetErrorMessage() == "" {
		t.Fatalf("%q %v", text, isErr)
	}
}

func TestGrepAndGlobResults(t *testing.T) {
	exec, ui, pending, err := Request(1, provider.ToolCall{ID: "g", Name: "Grep", Arguments: `{"pattern":"func main","-C":2,"-i":true,"output_mode":"content"}`})
	if err != nil {
		t.Fatal(err)
	}
	if g := exec.GetGrepArgs(); g.GetPattern() != "func main" || g.GetContext() != 2 || !g.GetCaseInsensitive() || g.GetOutputMode() != "content" {
		t.Fatalf("%v", g)
	}
	result := &cursorpb.GrepResult{Result: &cursorpb.GrepResult_Success{Success: &cursorpb.GrepSuccess{Pattern: "func main", Path: "/w",
		WorkspaceResults: map[string]*cursorpb.GrepUnionResult{"/w": {Result: &cursorpb.GrepUnionResult_Content{Content: &cursorpb.GrepContentResult{
			Matches: []*cursorpb.GrepFileMatch{{File: "main.go", Matches: []*cursorpb.GrepContentMatch{{LineNumber: 3, Content: "package main", IsContextLine: true}, {LineNumber: 5, Content: "func main() {"}}}},
		}}}}}}}
	text, isErr := pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_GrepResult{GrepResult: result}}, ui)
	if isErr || text != "main.go-3-package main\nmain.go:5:func main() {" || ui.GetGrepToolCall().GetResult() != result {
		t.Fatalf("%q", text)
	}

	exec, ui, pending, err = Request(2, provider.ToolCall{ID: "o", Name: "Glob", Arguments: `{"glob_pattern":"**/*.go","target_directory":"/w"}`})
	if err != nil {
		t.Fatal(err)
	}
	if g := exec.GetGrepArgs(); g.GetGlob() != "**/*.go" || g.GetPath() != "/w" || g.GetOutputMode() != "files_with_matches" || ui.GetGlobToolCall().GetArgs().GetGlobPattern() != "**/*.go" {
		t.Fatalf("%v", exec)
	}
	files := &cursorpb.GrepResult{Result: &cursorpb.GrepResult_Success{Success: &cursorpb.GrepSuccess{
		WorkspaceResults: map[string]*cursorpb.GrepUnionResult{"/w": {Result: &cursorpb.GrepUnionResult_Files{Files: &cursorpb.GrepFilesResult{Files: []string{"a.go", "b.go"}, TotalFiles: 2}}}},
	}}}
	text, _ = pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_GrepResult{GrepResult: files}}, ui)
	if text != "a.go\nb.go" || len(ui.GetGlobToolCall().GetResult().GetSuccess().GetFiles()) != 2 {
		t.Fatalf("%q %v", text, ui)
	}
}

func readResult(content string, truncated bool) *cursorpb.ExecClientMessage {
	return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_Success{Success: &cursorpb.ReadSuccess{Path: "/w/a.go", Truncated: truncated, Output: &cursorpb.ReadSuccess_Content{Content: content}}},
	}}}
}

func writeOK(path string) *cursorpb.ExecClientMessage {
	return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
		Result: &cursorpb.WriteResult_Success{Success: &cursorpb.WriteSuccess{Path: path, LinesCreated: 1}},
	}}}
}

func TestStrReplaceReadsThenWrites(t *testing.T) {
	exec, ui, pending, err := Request(1, provider.ToolCall{ID: "e1", Name: "StrReplace", Arguments: `{"path":"/w/a.go","old_string":"hello\r\n","new_string":"world\n"}`})
	if err != nil {
		t.Fatal(err)
	}
	if exec.GetReadArgs().GetPath() != "/w/a.go" || ui.GetEditToolCall().GetArgs().GetStreamContent() != "world\n" {
		t.Fatalf("first step %v", exec)
	}
	next, text, isErr := pending.Advance(readResult("say hello\r\nthere\n", false), ui)
	if next == nil || isErr || text != "" || next.GetWriteArgs().GetFileText() != "say world\nthere\n" {
		t.Fatalf("write %q %v %v", text, isErr, next)
	}
	_, text, isErr = pending.Advance(writeOK("/w/a.go"), ui)
	success := ui.GetEditToolCall().GetResult().GetSuccess()
	if isErr || text != "write success path=/w/a.go lines=1" || success.GetBeforeFullFileContent() != "say hello\nthere\n" || success.GetAfterFullFileContent() != "say world\nthere\n" || success.GetLinesAdded() != 1 || success.GetLinesRemoved() != 1 || !strings.Contains(success.GetDiffString(), "-say hello\n") {
		t.Fatalf("%q added=%d removed=%d diff=%q", text, success.GetLinesAdded(), success.GetLinesRemoved(), success.GetDiffString())
	}
}

func TestStrReplaceRejectsAmbiguousAndEmptyOld(t *testing.T) {
	_, ui, pending, err := Request(1, provider.ToolCall{ID: "e2", Name: "StrReplace", Arguments: `{"file_path":"/w/a.go","old_string":"a","new_string":"b"}`})
	if err != nil {
		t.Fatal(err)
	}
	next, text, isErr := pending.Advance(readResult("a\na\n", false), ui)
	if next != nil || !isErr || text != "old_string is not unique; found 2 occurrences" || ui.GetEditToolCall().GetResult().GetError().GetError() == "" {
		t.Fatalf("%v %q", next, text)
	}

	_, ui, pending, err = Request(2, provider.ToolCall{ID: "e3", Name: "StrReplace", Arguments: `{"path":"/w/a.go","old_string":"a","new_string":"b","replace_all":true}`})
	if err != nil {
		t.Fatal(err)
	}
	next, _, isErr = pending.Advance(readResult("a\na\n", false), ui)
	if isErr || next.GetWriteArgs().GetFileText() != "b\nb\n" {
		t.Fatalf("%v", next)
	}

	_, ui, pending, err = Request(3, provider.ToolCall{ID: "e4", Name: "StrReplace", Arguments: `{"path":"/w/a.go","old_string":"","new_string":"b"}`})
	if err != nil {
		t.Fatal(err)
	}
	next, text, isErr = pending.Advance(readResult("a\n", false), ui)
	if next != nil || !isErr || text != "old_string must not be empty" {
		t.Fatalf("%v %q", next, text)
	}
}

func TestWriteOfMissingFileAndRejection(t *testing.T) {
	exec, ui, pending, err := Request(4, provider.ToolCall{ID: "w1", Name: "Write", Arguments: `{"path":"/w/b.go","content":"package b\r\n"}`})
	if err != nil {
		t.Fatal(err)
	}
	if exec.GetReadArgs() == nil || ui.GetEditToolCall().GetArgs().GetStreamContent() != "package b\n" {
		t.Fatal(exec)
	}
	missing := &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_FileNotFound{FileNotFound: &cursorpb.ReadFileNotFound{Path: "/w/b.go"}},
	}}}
	next, _, isErr := pending.Advance(missing, ui)
	if isErr || next.GetWriteArgs().GetFileText() != "package b\n" || next.GetWriteArgs().GetReturnFileContentAfterWrite() {
		t.Fatalf("%v", next)
	}
	_, _, _ = pending.Advance(writeOK("/w/b.go"), ui)
	if ui.GetEditToolCall().GetResult().GetSuccess().GetBeforeFullFileContent() != "" || ui.GetEditToolCall().GetResult().GetSuccess().GetLinesAdded() != 1 {
		t.Fatalf("%v", ui.GetEditToolCall().GetResult())
	}

	_, ui, pending, err = Request(5, provider.ToolCall{ID: "w2", Name: "Write", Arguments: `{"path":"/w/a.go","contents":"x"}`})
	if err != nil {
		t.Fatal(err)
	}
	next, _, _ = pending.Advance(readResult("old\n", false), ui)
	_, text, isErr := pending.Advance(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
		Result: &cursorpb.WriteResult_Rejected{Rejected: &cursorpb.WriteRejected{Path: "/w/a.go", Reason: "user said no"}},
	}}}, ui)
	if next == nil || !isErr || text != "user said no" || ui.GetEditToolCall().GetResult().GetRejected().GetReason() != "user said no" {
		t.Fatalf("%v %q", next, text)
	}
}

func TestDeleteRoundTrip(t *testing.T) {
	exec, ui, pending, err := Request(6, provider.ToolCall{ID: "d1", Name: "Delete", Arguments: `{"filePath":"/w/a.go"}`})
	if err != nil {
		t.Fatal(err)
	}
	if exec.GetDeleteArgs().GetPath() != "/w/a.go" || exec.GetDeleteArgs().GetToolCallId() != "d1" || ui.GetDeleteToolCall().GetArgs().GetPath() != "/w/a.go" {
		t.Fatalf("%v", exec)
	}
	result := &cursorpb.DeleteResult{Result: &cursorpb.DeleteResult_Success{Success: &cursorpb.DeleteSuccess{Path: "/w/a.go"}}}
	text, isErr := pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_DeleteResult{DeleteResult: result}}, ui)
	if isErr || text != "delete success path=/w/a.go" || ui.GetDeleteToolCall().GetResult() != result {
		t.Fatalf("%q %v", text, isErr)
	}
	text, isErr = pending.Result(&cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_DeleteResult{DeleteResult: &cursorpb.DeleteResult{
		Result: &cursorpb.DeleteResult_Rejected{Rejected: &cursorpb.DeleteRejected{Path: "/w/a.go", Reason: "kept"}},
	}}}, ui)
	if !isErr || text != "kept" {
		t.Fatalf("%q", text)
	}
}

func TestUnknownToolAndBadArguments(t *testing.T) {
	if _, _, _, err := Request(1, provider.ToolCall{ID: "x", Name: "Teleport", Arguments: "{}"}); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if _, _, _, err := Request(1, provider.ToolCall{ID: "x", Name: "Read", Arguments: "{bad"}); err == nil {
		t.Fatal("bad json accepted")
	}
	if _, _, _, err := Request(1, provider.ToolCall{ID: "x", Name: "Read", Arguments: "{}"}); err == nil {
		t.Fatal("missing path accepted")
	}
	if _, _, _, err := Request(1, provider.ToolCall{ID: "x", Name: "StrReplace", Arguments: `{"path":"/w/a.go"}`}); err == nil {
		t.Fatal("strreplace without old_string accepted")
	}
	if _, _, _, err := Request(1, provider.ToolCall{ID: "x", Name: "Write", Arguments: `{"path":"/w/a.go"}`}); err == nil {
		t.Fatal("write without contents accepted")
	}
}

func TestEditNotebookReplacesAndInserts(t *testing.T) {
	before := "{\n \"cells\": [{\"cell_type\": \"code\", \"metadata\": {}, \"source\": [\"print(1)\\n\"]}]\n}\n"
	after, err := editNotebook(args{"old_string": "print(1)", "new_string": "print(2)\n"}, before)
	if err != nil || !strings.Contains(after, "print(2)") || strings.Contains(after, "print(1)") {
		t.Fatalf("%v %s", err, after)
	}
	inserted, err := editNotebook(args{"is_new_cell": true, "cell_idx": float64(0), "cell_language": "markdown", "new_string": "title\n"}, after)
	if err != nil || !strings.Contains(inserted, `"cell_type": "markdown"`) {
		t.Fatalf("%v %s", err, inserted)
	}
	if _, err := editNotebook(args{"old_string": "", "new_string": "x"}, after); err == nil {
		t.Fatal("empty old_string accepted")
	}
}

func TestSelectSnippetsIgnoresInventedPaths(t *testing.T) {
	snips := []Snippet{{Path: "/w/a.go", Line: "func real"}}
	got := SelectSnippets(snips, "99\n/tmp/made-up.go")
	if strings.Contains(got, "made-up") || strings.Contains(got, "a.go") {
		t.Fatalf("%s", got)
	}
	got = SelectSnippets(snips, "1")
	if !strings.Contains(got, "/w/a.go") || !strings.Contains(got, "func real") {
		t.Fatalf("%s", got)
	}
}

func TestAskQuestionFormatsTheAnswer(t *testing.T) {
	query, ui, err, ok := BeginInteraction(provider.ToolCall{ID: "q", Name: "AskQuestion", Arguments: `{"title":"Pick","questions":[{"id":"q1","prompt":"Which?","options":[{"id":"a","label":"Alpha"}]}]}`})
	if !ok || err != nil || query.GetAskQuestionInteractionQuery() == nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	text, isErr, mode := CompleteInteraction(ui, &cursorpb.InteractionResponse{Result: &cursorpb.InteractionResponse_AskQuestionInteractionResponse{AskQuestionInteractionResponse: &cursorpb.AskQuestionInteractionResponse{
		Result: &cursorpb.AskQuestionResult{Result: &cursorpb.AskQuestionResult_Success{Success: &cursorpb.AskQuestionSuccess{Answers: []*cursorpb.AskQuestionSuccess_Answer{{QuestionId: "q1", SelectedOptionIds: []string{"a"}}}}}},
	}}}, ImageAPI{}, nil)
	if isErr || mode != "" || !strings.Contains(text, "q1: a") {
		t.Fatalf("%v %q", isErr, text)
	}
}

func TestAskQuestionAssignsIDsWhenTheModelOmitsThem(t *testing.T) {
	query, _, err, ok := BeginInteraction(provider.ToolCall{ID: "q", Name: "AskQuestion", Arguments: `{"questions":[{"prompt":"Which?","options":[{"label":"A red"}]}]}`})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	questions := query.GetAskQuestionInteractionQuery().GetArgs().GetQuestions()
	if len(questions) != 1 || questions[0].GetId() != "q1" || questions[0].GetPrompt() != "Which?" {
		t.Fatalf("%+v", questions)
	}
	if len(questions[0].GetOptions()) != 1 || questions[0].GetOptions()[0].GetId() != "A red" || strings.Contains(questions[0].GetOptions()[0].GetId(), "<nil>") {
		t.Fatalf("%+v", questions[0].GetOptions())
	}
}

func TestMCPExecUsesTheDefinition(t *testing.T) {
	def := &cursorpb.McpToolDefinition{Name: "srv/ping", ProviderIdentifier: "srv", ToolName: "ping", Description: "ping"}
	call := provider.ToolCall{ID: "m", Name: "CallMcpTool", Arguments: `{"server":"srv","toolName":"ping","arguments":{"host":"local"}}`}
	matched, ok := MatchMCP(call, []*cursorpb.McpToolDefinition{def})
	if !ok || matched.GetToolName() != "ping" {
		t.Fatalf("%v", matched)
	}
	exec, _, _, err := MCPExec(4, call, matched)
	if err != nil || exec.GetMcpArgs().GetToolName() != "ping" || exec.GetMcpArgs().GetArgs()["host"].GetStringValue() != "local" {
		t.Fatalf("%v %+v", err, exec.GetMcpArgs())
	}
}

func TestGenerateImageDownloadsURL(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(png)
	}))
	defer file.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"` + file.URL + `"}]}`))
	}))
	defer api.Close()
	got, err := generateImage(dialer.Direct(), ImageAPI{BaseURL: api.URL, Model: "img"}, "a cat")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil || string(raw) != string(png) {
		t.Fatalf("%q %v", got, err)
	}
}

func TestBrowserFileURLNeverStarts(t *testing.T) {
	def := &cursorpb.McpToolDefinition{Name: "cursor-ide-browser-browser_navigate", ToolName: "browser_navigate", ProviderIdentifier: "cursor-ide-browser"}
	call := provider.ToolCall{ID: "b", Name: "browser_navigate", Arguments: `{"url":"file:///data/map.png"}`}
	if _, _, _, err := MCPExec(1, call, def); !errors.Is(err, ErrBrowserFileURL) {
		t.Fatal(err)
	}
	web := provider.ToolCall{ID: "w", Name: "browser_navigate", Arguments: `{"url":"https://example.com"}`}
	if _, _, _, err := MCPExec(2, web, def); err != nil {
		t.Fatal(err)
	}
}

func TestTaskWaitIsBounded(t *testing.T) {
	_, _, pending, err := Request(1, provider.ToolCall{ID: "t", Name: "Task", Arguments: `{"description":"look","prompt":"read the file"}`})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Wait != 10*time.Minute {
		t.Fatal(pending.Wait)
	}
}

func TestLongShellIsBackgroundedNotKilled(t *testing.T) {
	exec, _, pending, err := Request(1, provider.ToolCall{ID: "s", Name: "Shell", Arguments: `{"command":"sleep 10","block_until_ms":7200000}`})
	if err != nil {
		t.Fatal(err)
	}
	args := exec.GetShellStreamArgs()
	if args.HardTimeout != nil || args.GetTimeout() != foregroundMaxMs {
		t.Fatalf("timeout=%d hard=%v", args.GetTimeout(), args.HardTimeout)
	}
	if pending.Wait != time.Duration(foregroundMaxMs)*time.Millisecond+15*time.Second || pending.Note == "" {
		t.Fatalf("wait=%s note=%q", pending.Wait, pending.Note)
	}
	msg := &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ShellStream{ShellStream: &cursorpb.ShellStream{
		Event: &cursorpb.ShellStream_Backgrounded{Backgrounded: &cursorpb.ShellStreamBackgrounded{ShellId: 7, Command: "sleep 10"}},
	}}}
	_, _, text, isErr, done := pending.Feed(msg, nil)
	if !done || isErr || !strings.Contains(text, "not killed") || !strings.Contains(text, "shell_id=7") {
		t.Fatalf("done=%v err=%v text=%q", done, isErr, text)
	}
}
