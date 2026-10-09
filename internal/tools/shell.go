package tools

import (
	"fmt"
	"strings"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

type shellState struct {
	stdout strings.Builder
	stderr strings.Builder
}

func startShell(id uint32, call provider.ToolCall, a args) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	command, ok := a.str("command")
	if !ok {
		return nil, nil, nil, fmt.Errorf("Shell requires command")
	}
	timeout := int32(30_000)
	if n := a.int32("block_until_ms"); n != nil {
		if *n < 0 {
			return nil, nil, nil, fmt.Errorf("Shell block_until_ms is out of range")
		}
		timeout = *n
	}
	requested := timeout
	if timeout > foregroundMaxMs {
		timeout = foregroundMaxMs
	}
	threshold := uint64(40_000)
	shell := &cursorpb.ShellArgs{
		Command:                  command,
		WorkingDirectory:         stringOr(a, "working_directory"),
		Timeout:                  timeout,
		ToolCallId:               call.ID,
		SimpleCommands:           []string{strings.TrimSpace(command)},
		ParsingResult:            shellParsing(command),
		RequestedSandboxPolicy:   shellSandbox(a),
		FileOutputThresholdBytes: &threshold,
		TimeoutBehavior:          cursorpb.TimeoutBehavior_TIMEOUT_BEHAVIOR_BACKGROUND,
		Description:              a.optStr("description"),
		CloseStdin:               true,
		OutputNotification:       shellNotification(a),
		SmartModeApproval:        shellApproval(call.ID, a),
	}
	exec := &cursorpb.ExecServerMessage{
		Id:      id,
		ExecId:  fmt.Sprintf("%s-%d", call.ID, id),
		Message: &cursorpb.ExecServerMessage_ShellStreamArgs{ShellStreamArgs: shell},
	}
	ui := &cursorpb.ToolCall{
		ToolCallId: &call.ID,
		Tool: &cursorpb.ToolCall_ShellToolCall{ShellToolCall: &cursorpb.ShellToolCall{
			Description: shell.Description,
			Args:        shell,
		}},
	}
	pending := &Pending{Call: call, shell: &shellState{}, Wait: shellWait(timeout)}
	if requested > foregroundMaxMs {
		pending.Note = "The foreground wait is capped at 10 minutes. The command continues in the background and is not killed when that wait ends."
	}
	return exec, ui, pending, nil
}

// foregroundMaxMs 是一轮里最多前台等待的时间。
// 更长的命令改为后台继续跑，不设硬超时，所以以天计的命令不会被掐掉。
const foregroundMaxMs = int32(600_000)

// shellWait 是这条命令在没有新输出时，本地最多再等多久。
// 客户端会在这段前台时间后转入后台；这里多留一点，让那条结果能回来。
func shellWait(blockUntilMs int32) time.Duration {
	wait := time.Duration(blockUntilMs)*time.Millisecond + 15*time.Second
	if wait < 45*time.Second {
		return 45 * time.Second
	}
	return wait
}

func shellParsing(command string) *cursorpb.ShellCommandParsingResult {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	args := make([]*cursorpb.ShellCommandParsingResult_ExecutableCommandArg, 0, len(fields)-1)
	for _, field := range fields[1:] {
		args = append(args, &cursorpb.ShellCommandParsingResult_ExecutableCommandArg{Type: "word", Value: field})
	}
	return &cursorpb.ShellCommandParsingResult{ExecutableCommands: []*cursorpb.ShellCommandParsingResult_ExecutableCommand{{
		Name:     fields[0],
		Args:     args,
		FullText: strings.TrimSpace(command),
	}}}
}

func shellSandbox(a args) *cursorpb.SandboxPolicy {
	raw, _ := a["required_permissions"].([]any)
	var all, network bool
	for _, item := range raw {
		switch item {
		case "all":
			all = true
		case "full_network":
			network = true
		}
	}
	yes := true
	switch {
	case all:
		return &cursorpb.SandboxPolicy{Type: cursorpb.SandboxPolicy_TYPE_INSECURE_NONE, NetworkAccess: &yes}
	case network:
		return &cursorpb.SandboxPolicy{Type: cursorpb.SandboxPolicy_TYPE_WORKSPACE_READWRITE, NetworkAccess: &yes}
	default:
		return nil
	}
}

func shellNotification(a args) *cursorpb.ShellOutputNotificationConfig {
	obj, _ := a["notify_on_output"].(map[string]any)
	if obj == nil {
		return nil
	}
	pattern, _ := obj["pattern"].(string)
	reason, _ := obj["reason"].(string)
	if pattern == "" || reason == "" {
		return nil
	}
	note := &cursorpb.ShellOutputNotificationConfig{Pattern: pattern, Reason: reason}
	if ms, ok := obj["debounce_ms"].(float64); ok {
		note.Debounce = &ms
	}
	return note
}

func shellApproval(callID string, a args) *cursorpb.SmartModeApproval {
	if on, _ := a["request_smart_mode_approval"].(bool); !on {
		return nil
	}
	reason, _ := a["smart_mode_block_reason"].(string)
	if reason == "" {
		return nil
	}
	return &cursorpb.SmartModeApproval{RequestId: callID, Reason: reason}
}

func stringOr(a args, name string) string {
	if v, ok := a.str(name); ok {
		return v
	}
	return ""
}

// Feed 消化一条执行结果。Shell 的标准输出返回界面增量且 Done 为 false；结束时 Done 为 true。
func (p *Pending) Feed(msg *cursorpb.ExecClientMessage, ui *cursorpb.ToolCall) (next *cursorpb.ExecServerMessage, deltas []*cursorpb.AgentServerMessage, text string, isErr, done bool) {
	if mcp := msg.GetMcpResult(); mcp != nil && mcp.GetApproved() != nil {
		return nil, nil, "", false, false
	}
	stream := msg.GetShellStream()
	if stream == nil || p.shell == nil {
		next, text, isErr = p.Advance(msg, ui)
		return next, nil, text, isErr, next == nil
	}
	switch event := stream.GetEvent().(type) {
	case *cursorpb.ShellStream_Stdout:
		p.shell.stdout.WriteString(event.Stdout.GetData())
		return nil, []*cursorpb.AgentServerMessage{shellDelta(p.Call.ID, true, event.Stdout.GetData())}, "", false, false
	case *cursorpb.ShellStream_Stderr:
		p.shell.stderr.WriteString(event.Stderr.GetData())
		return nil, []*cursorpb.AgentServerMessage{shellDelta(p.Call.ID, false, event.Stderr.GetData())}, "", false, false
	case *cursorpb.ShellStream_Start, *cursorpb.ShellStream_HookContext:
		return nil, nil, "", false, false
	case *cursorpb.ShellStream_Exit:
		result := exitResult(event.Exit, p.shell.stdout.String(), p.shell.stderr.String())
		text, isErr = shellText(result)
		setShellUI(ui, result)
		return nil, nil, text, isErr, true
	case *cursorpb.ShellStream_Backgrounded:
		result := backgroundResult(event.Backgrounded, p.shell.stdout.String(), p.shell.stderr.String(), p.Terminals)
		text, isErr = shellText(result)
		if p.Note != "" {
			text = p.Note + "\n" + text
		}
		setShellUI(ui, result)
		return nil, nil, text, isErr, true
	case *cursorpb.ShellStream_Rejected:
		result := &cursorpb.ShellResult{Result: &cursorpb.ShellResult_Rejected{Rejected: event.Rejected}}
		text, isErr = shellText(result)
		setShellUI(ui, result)
		return nil, nil, text, isErr, true
	case *cursorpb.ShellStream_PermissionDenied:
		result := &cursorpb.ShellResult{Result: &cursorpb.ShellResult_PermissionDenied{PermissionDenied: event.PermissionDenied}}
		text, isErr = shellText(result)
		setShellUI(ui, result)
		return nil, nil, text, isErr, true
	case *cursorpb.ShellStream_SandboxUnsupported:
		result := &cursorpb.ShellResult{Result: &cursorpb.ShellResult_SpawnError{SpawnError: &cursorpb.ShellSpawnError{
			Command:          event.SandboxUnsupported.GetCommand(),
			WorkingDirectory: event.SandboxUnsupported.GetWorkingDirectory(),
			Error:            event.SandboxUnsupported.GetReason(),
		}}}
		text, isErr = shellText(result)
		setShellUI(ui, result)
		return nil, nil, text, isErr, true
	default:
		return nil, nil, "Cursor returned an empty shell event", true, true
	}
}

func shellDelta(callID string, stdout bool, content string) *cursorpb.AgentServerMessage {
	delta := &cursorpb.ShellOutputDeltaUpdate{}
	if stdout {
		delta.Event = &cursorpb.ShellOutputDeltaUpdate_Stdout{Stdout: &cursorpb.ShellStreamStdout{Data: content}}
	} else {
		delta.Event = &cursorpb.ShellOutputDeltaUpdate_Stderr{Stderr: &cursorpb.ShellStreamStderr{Data: content}}
	}
	return &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: &cursorpb.InteractionUpdate{
		Message: &cursorpb.InteractionUpdate_ShellOutputDelta{ShellOutputDelta: delta},
	}}}
}

func exitResult(exit *cursorpb.ShellStreamExit, stdout, stderr string) *cursorpb.ShellResult {
	interleaved := stdout + stderr
	if exit.GetCode() == 0 && !exit.GetAborted() {
		return &cursorpb.ShellResult{IsBackground: boolPtr(false), Result: &cursorpb.ShellResult_Success{Success: &cursorpb.ShellSuccess{
			WorkingDirectory:     exit.GetCwd(),
			ExitCode:             int32(exit.GetCode()),
			Stdout:               stdout,
			Stderr:               stderr,
			InterleavedOutput:    &interleaved,
			LocalExecutionTimeMs: exit.LocalExecutionTimeMs,
		}}}
	}
	return &cursorpb.ShellResult{IsBackground: boolPtr(false), Result: &cursorpb.ShellResult_Failure{Failure: &cursorpb.ShellFailure{
		WorkingDirectory:     exit.GetCwd(),
		ExitCode:             int32(exit.GetCode()),
		Stdout:               stdout,
		Stderr:               stderr,
		InterleavedOutput:    &interleaved,
		Aborted:              exit.GetAborted(),
		AbortReason:          exit.AbortReason,
		LocalExecutionTimeMs: exit.LocalExecutionTimeMs,
	}}}
}

func backgroundResult(bg *cursorpb.ShellStreamBackgrounded, stdout, stderr, terminals string) *cursorpb.ShellResult {
	interleaved := stdout + stderr
	shellID := bg.GetShellId()
	result := &cursorpb.ShellResult{
		IsBackground: boolPtr(true),
		Pid:          bg.Pid,
		Result: &cursorpb.ShellResult_Success{Success: &cursorpb.ShellSuccess{
			Command:           bg.GetCommand(),
			WorkingDirectory:  bg.GetWorkingDirectory(),
			Stdout:            stdout,
			Stderr:            stderr,
			ShellId:           &shellID,
			Pid:               bg.Pid,
			MsToWait:          bg.MsToWait,
			BackgroundReason:  bg.Reason,
			InterleavedOutput: &interleaved,
		}},
	}
	if terminals != "" {
		result.TerminalsFolder = &terminals
	}
	return result
}

func setShellUI(ui *cursorpb.ToolCall, result *cursorpb.ShellResult) {
	if tool, ok := ui.GetTool().(*cursorpb.ToolCall_ShellToolCall); ok {
		tool.ShellToolCall.Result = result
	}
}

func shellText(result *cursorpb.ShellResult) (string, bool) {
	switch v := result.GetResult().(type) {
	case *cursorpb.ShellResult_Success:
		if result.GetIsBackground() {
			fields := []string{fmt.Sprintf("shell_id=%d", v.Success.GetShellId())}
			if pid := v.Success.GetPid(); pid != 0 {
				fields = append(fields, fmt.Sprintf("pid=%d", pid))
			}
			if folder := result.GetTerminalsFolder(); folder != "" {
				fields = append(fields, "terminals_folder="+folder)
			}
			output := shellStreams(v.Success.GetStdout(), v.Success.GetStderr())
			prefix := "shell running in background " + strings.Join(fields, " ")
			if output == "shell completed without output" {
				return prefix, false
			}
			return prefix + "\n" + output, false
		}
		return shellStreams(v.Success.GetStdout(), v.Success.GetStderr()), false
	case *cursorpb.ShellResult_Failure:
		return shellStreams(v.Failure.GetStdout(), v.Failure.GetStderr()), true
	case *cursorpb.ShellResult_Timeout:
		return fmt.Sprintf("shell timed out after %dms in %s", v.Timeout.GetTimeoutMs(), v.Timeout.GetWorkingDirectory()), true
	case *cursorpb.ShellResult_Rejected:
		return v.Rejected.GetReason(), true
	case *cursorpb.ShellResult_SpawnError:
		return v.SpawnError.GetError(), true
	case *cursorpb.ShellResult_PermissionDenied:
		return v.PermissionDenied.GetError(), true
	default:
		return "Cursor returned an empty shell result", true
	}
}

func shellStreams(stdout, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n\n<stderr>\n" + stderr + "\n</stderr>"
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return "shell completed without output"
	}
}

func boolPtr(v bool) *bool { return &v }
