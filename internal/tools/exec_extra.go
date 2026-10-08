package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func startTask(id uint32, call provider.ToolCall, a args) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	prompt, ok := a.str("prompt")
	if !ok {
		return nil, nil, nil, fmt.Errorf("Task requires prompt")
	}
	kind, _ := a.str("subagent_type", "subagentType")
	if kind == "" {
		kind = "generalPurpose"
	}
	env := cursorpb.SubagentExecutionEnvironment_SUBAGENT_EXECUTION_ENVIRONMENT_LOCAL
	if value, _ := a.str("environment"); value == "cloud" {
		env = cursorpb.SubagentExecutionEnvironment_SUBAGENT_EXECUTION_ENVIRONMENT_CLOUD
	}
	model, _ := a.str("model")
	desc, _ := a.str("description")
	sub := &cursorpb.SubagentArgs{
		ToolCallId:      call.ID,
		SubagentType:    kind,
		ModelId:         model,
		Prompt:          prompt,
		ResumeAgentId:   a.optStr("resume"),
		RunInBackground: a.bool("run_in_background"),
		Environment:     env,
	}
	exec := &cursorpb.ExecServerMessage{
		Id:      id,
		ExecId:  fmt.Sprintf("%s-%d", call.ID, id),
		Message: &cursorpb.ExecServerMessage_SubagentArgs{SubagentArgs: sub},
	}
	ui := &cursorpb.ToolCall{ToolCallId: &call.ID, Tool: &cursorpb.ToolCall_TaskToolCall{TaskToolCall: &cursorpb.TaskToolCall{
		Args: &cursorpb.TaskArgs{
			Description:  desc,
			Prompt:       prompt,
			Model:        a.optStr("model"),
			Resume:       a.optStr("resume"),
			Environment:  env,
			SubagentType: &cursorpb.SubagentType{Type: &cursorpb.SubagentType_Custom{Custom: &cursorpb.SubagentTypeCustom{Name: kind}}},
		},
	}}}
	return exec, ui, &Pending{Call: call}, nil
}

func startFetchResource(id uint32, call provider.ToolCall, a args) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	server, ok := a.str("server")
	if !ok {
		return nil, nil, nil, fmt.Errorf("FetchMcpResource requires server")
	}
	uri, ok := a.str("uri")
	if !ok {
		return nil, nil, nil, fmt.Errorf("FetchMcpResource requires uri")
	}
	args := &cursorpb.ReadMcpResourceExecArgs{Server: server, Uri: uri, DownloadPath: a.optStr("downloadPath"), ToolCallId: call.ID}
	if args.DownloadPath == nil {
		args.DownloadPath = a.optStr("download_path")
	}
	exec := &cursorpb.ExecServerMessage{
		Id:      id,
		ExecId:  fmt.Sprintf("%s-%d", call.ID, id),
		Message: &cursorpb.ExecServerMessage_ReadMcpResourceExecArgs{ReadMcpResourceExecArgs: args},
	}
	ui := &cursorpb.ToolCall{ToolCallId: &call.ID, Tool: &cursorpb.ToolCall_ReadMcpResourceToolCall{ReadMcpResourceToolCall: &cursorpb.ReadMcpResourceToolCall{Args: args}}}
	return exec, ui, &Pending{Call: call}, nil
}

// MatchMCP 判断这次调用是不是 MCP。CallMcpTool 和请求上下文里声明的动态工具名都算。
func MatchMCP(call provider.ToolCall, defs []*cursorpb.McpToolDefinition) (*cursorpb.McpToolDefinition, bool) {
	name := normalize(call.Name)
	if name == "callmcptool" {
		a, err := parseArgs(call.Arguments)
		if err != nil {
			return nil, true
		}
		server, _ := a.str("server", "serverIdentifier", "server_identifier")
		tool, _ := a.str("toolName", "tool_name", "name")
		for _, def := range defs {
			if def == nil {
				continue
			}
			if server != "" && !mcpServerMatch(def, server) {
				continue
			}
			if tool == "" || strings.EqualFold(def.GetToolName(), tool) || strings.EqualFold(def.GetName(), tool) {
				return def, true
			}
		}
		return nil, true
	}
	for _, def := range defs {
		if def == nil {
			continue
		}
		if strings.EqualFold(def.GetName(), call.Name) || strings.EqualFold(def.GetToolName(), call.Name) {
			return def, true
		}
	}
	return nil, false
}

func mcpServerMatch(def *cursorpb.McpToolDefinition, server string) bool {
	if strings.EqualFold(def.GetProviderIdentifier(), server) || strings.EqualFold(def.GetName(), server) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(def.GetName()), strings.ToLower(server)+"-")
}

func mcpServerID(def *cursorpb.McpToolDefinition) string {
	suffix := "-" + def.GetToolName()
	if def.GetToolName() != "" && strings.HasSuffix(def.GetName(), suffix) {
		return strings.TrimSuffix(def.GetName(), suffix)
	}
	return ""
}

// MCPExec 把一次 MCP 调用交给 Cursor 执行。
func MCPExec(id uint32, call provider.ToolCall, def *cursorpb.McpToolDefinition) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return nil, nil, nil, err
	}
	if def == nil {
		return nil, nil, nil, fmt.Errorf("MCP tool %s is not in this conversation", call.Name)
	}
	payload := map[string]any(a)
	if nested, ok := a["arguments"].(map[string]any); ok && normalize(call.Name) == "callmcptool" {
		payload = nested
	}
	values, err := protoMap(payload)
	if err != nil {
		return nil, nil, nil, err
	}
	mcp := &cursorpb.McpArgs{
		Name:               def.GetName(),
		Args:               values,
		ToolCallId:         call.ID,
		ProviderIdentifier: def.GetProviderIdentifier(),
		ToolName:           def.GetToolName(),
		ServerIdentifier:   mcpServerID(def),
	}
	exec := &cursorpb.ExecServerMessage{
		Id:      id,
		ExecId:  fmt.Sprintf("%s-%d", call.ID, id),
		Message: &cursorpb.ExecServerMessage_McpArgs{McpArgs: mcp},
	}
	ui := &cursorpb.ToolCall{ToolCallId: &call.ID, Tool: &cursorpb.ToolCall_McpToolCall{McpToolCall: &cursorpb.McpToolCall{Args: mcp}}}
	return exec, ui, &Pending{Call: call}, nil
}

// MCPDefs 合并请求上下文里直接列出的工具，以及文件描述符里的工具。
func MCPDefs(ctx *cursorpb.RequestContext) []*cursorpb.McpToolDefinition {
	if ctx == nil {
		return nil
	}
	out := append([]*cursorpb.McpToolDefinition{}, ctx.GetTools()...)
	seen := map[string]bool{}
	for _, def := range out {
		if def != nil {
			seen[def.GetProviderIdentifier()+"/"+def.GetToolName()] = true
		}
	}
	var descriptors []*cursorpb.McpDescriptor
	if opts := ctx.GetMcpMetaToolOptions(); opts != nil {
		descriptors = append(descriptors, opts.GetMcpDescriptors()...)
	}
	if opts := ctx.GetMcpFileSystemOptions(); opts != nil {
		descriptors = append(descriptors, opts.GetMcpDescriptors()...)
	}
	for _, server := range descriptors {
		for _, tool := range server.GetTools() {
			name := tool.GetToolName()
			if name == "" {
				continue
			}
			key := server.GetServerIdentifier() + "/" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			providerID := server.GetServerName()
			if providerID == "" {
				providerID = server.GetServerIdentifier()
			}
			def := &cursorpb.McpToolDefinition{
				Name:               server.GetServerIdentifier() + "-" + name,
				ToolName:           name,
				ProviderIdentifier: providerID,
				Description:        tool.GetDescription(),
			}
			if schema := tool.GetInputSchemaJson(); schema != "" {
				def.InputSchemaJson = &schema
			}
			out = append(out, def)
		}
	}
	return out
}

// MCPCatalog 把请求上下文里的 MCP 工具加进模型可见的工具表。
func MCPCatalog(defs []*cursorpb.McpToolDefinition) []provider.Tool {
	var out []provider.Tool
	for _, def := range defs {
		if def == nil {
			continue
		}
		name := def.GetToolName()
		if name == "" {
			name = def.GetName()
		}
		if name == "" {
			continue
		}
		schema := strings.TrimSpace(def.GetInputSchemaJson())
		if !json.Valid([]byte(schema)) {
			schema = `{"type":"object"}`
		}
		out = append(out, provider.Tool{Name: name, Description: def.GetDescription(), Parameters: []byte(schema)})
	}
	return out
}

// ListMCP 在本地列出这次对话可用的 MCP 工具。
func ListMCP(call provider.ToolCall, defs []*cursorpb.McpToolDefinition) (string, bool, *cursorpb.ToolCall, bool) {
	if normalize(call.Name) != "getmcptools" {
		return "", false, nil, false
	}
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return err.Error(), true, nil, true
	}
	server, _ := a.str("server")
	pattern, _ := a.str("pattern")
	var lines []string
	for _, def := range defs {
		if def == nil {
			continue
		}
		if server != "" && !strings.EqualFold(def.GetProviderIdentifier(), server) && !strings.EqualFold(def.GetName(), server) {
			continue
		}
		name := def.GetToolName()
		if name == "" {
			name = def.GetName()
		}
		if pattern != "" && !strings.Contains(strings.ToLower(name+" "+def.GetDescription()), strings.ToLower(pattern)) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s (%s): %s", name, def.GetProviderIdentifier(), def.GetDescription()))
	}
	text := "No MCP tools are available in this conversation."
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	ui := &cursorpb.ToolCall{ToolCallId: &call.ID, Tool: &cursorpb.ToolCall_GetMcpToolsToolCall{GetMcpToolsToolCall: &cursorpb.GetMcpToolsToolCall{
		Args:   &cursorpb.GetMcpToolsArgs{ToolCallId: call.ID},
		Result: &cursorpb.GetMcpToolsAgentResult{Result: &cursorpb.GetMcpToolsAgentResult_Success{Success: &cursorpb.GetMcpToolsSuccess{Content: text}}},
	}}}
	return text, false, ui, true
}

func protoMap(raw map[string]any) (map[string]*structpb.Value, error) {
	out := make(map[string]*structpb.Value, len(raw))
	for key, value := range raw {
		converted, err := structpb.NewValue(value)
		if err != nil {
			return nil, fmt.Errorf("argument %s: %w", key, err)
		}
		out[key] = converted
	}
	return out, nil
}

func mcpText(r *cursorpb.McpResult) (string, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.McpResult_Success:
		var lines []string
		for _, item := range v.Success.GetContent() {
			if text := item.GetText(); text != nil && text.GetText() != "" {
				lines = append(lines, text.GetText())
			}
		}
		if len(lines) == 0 {
			return "MCP tool returned no text.", v.Success.GetIsError()
		}
		return strings.Join(lines, "\n"), v.Success.GetIsError()
	case *cursorpb.McpResult_Error:
		return v.Error.GetError(), true
	case *cursorpb.McpResult_Rejected:
		return "The user rejected the MCP call: " + v.Rejected.GetReason(), true
	case *cursorpb.McpResult_PermissionDenied:
		return v.PermissionDenied.GetError(), true
	case *cursorpb.McpResult_ToolNotFound:
		return "MCP tool not found: " + v.ToolNotFound.GetName(), true
	case *cursorpb.McpResult_ServerNotFound:
		return "MCP server not found: " + v.ServerNotFound.GetName(), true
	}
	return "Cursor returned an empty MCP result", true
}

func mcpUI(r *cursorpb.McpResult) *cursorpb.McpToolResult {
	switch v := r.GetResult().(type) {
	case *cursorpb.McpResult_Success:
		return &cursorpb.McpToolResult{Result: &cursorpb.McpToolResult_Success{Success: v.Success}}
	case *cursorpb.McpResult_Error:
		return &cursorpb.McpToolResult{Result: &cursorpb.McpToolResult_Error{Error: &cursorpb.McpToolError{Error: v.Error.GetError()}}}
	case *cursorpb.McpResult_Rejected:
		return &cursorpb.McpToolResult{Result: &cursorpb.McpToolResult_Rejected{Rejected: v.Rejected}}
	case *cursorpb.McpResult_PermissionDenied:
		return &cursorpb.McpToolResult{Result: &cursorpb.McpToolResult_PermissionDenied{PermissionDenied: v.PermissionDenied}}
	default:
		text, _ := mcpText(r)
		return &cursorpb.McpToolResult{Result: &cursorpb.McpToolResult_Error{Error: &cursorpb.McpToolError{Error: text}}}
	}
}

func subagentText(r *cursorpb.SubagentResult) (string, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.SubagentResult_Success:
		if v.Success.GetFinalMessage() != "" {
			return v.Success.GetFinalMessage(), false
		}
		return "Subagent " + v.Success.GetAgentId() + " finished.", false
	case *cursorpb.SubagentResult_Error:
		return v.Error.GetError(), true
	}
	return "Cursor returned an empty subagent result", true
}

func taskUI(r *cursorpb.SubagentResult) *cursorpb.TaskResult {
	switch v := r.GetResult().(type) {
	case *cursorpb.SubagentResult_Success:
		id := v.Success.GetAgentId()
		success := &cursorpb.TaskSuccess{}
		if id != "" {
			success.AgentId = &id
		}
		return &cursorpb.TaskResult{Result: &cursorpb.TaskResult_Success{Success: success}}
	case *cursorpb.SubagentResult_Error:
		return &cursorpb.TaskResult{Result: &cursorpb.TaskResult_Error{Error: &cursorpb.TaskError{Error: v.Error.GetError()}}}
	default:
		return &cursorpb.TaskResult{Result: &cursorpb.TaskResult_Error{Error: &cursorpb.TaskError{Error: "empty subagent result"}}}
	}
}

func resourceText(r *cursorpb.ReadMcpResourceExecResult) (string, bool) {
	switch v := r.GetResult().(type) {
	case *cursorpb.ReadMcpResourceExecResult_Success:
		if v.Success.GetText() != "" {
			return v.Success.GetText(), false
		}
		if len(v.Success.GetBlob()) > 0 {
			return fmt.Sprintf("%s is binary (%d bytes).", v.Success.GetUri(), len(v.Success.GetBlob())), false
		}
		return "Fetched " + v.Success.GetUri(), false
	case *cursorpb.ReadMcpResourceExecResult_Error:
		return v.Error.GetError(), true
	case *cursorpb.ReadMcpResourceExecResult_Rejected:
		return "The user rejected reading " + v.Rejected.GetUri() + ": " + v.Rejected.GetReason(), true
	case *cursorpb.ReadMcpResourceExecResult_NotFound:
		return "MCP resource not found: " + v.NotFound.GetUri(), true
	}
	return "Cursor returned an empty MCP resource result", true
}
