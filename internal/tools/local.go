package tools

import (
	"fmt"
	"strings"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// FinishLocal 完成不需要 Cursor 执行的工具。ok 为 false 时调用方按普通工具下发。
func FinishLocal(call provider.ToolCall) (string, bool, *cursorpb.ToolCall, bool) {
	switch normalize(call.Name) {
	case "todowrite":
		return finishTodos(call)
	case "updatecurrentstep":
		return finishStep(call)
	default:
		return "", false, nil, false
	}
}

func finishStep(call provider.ToolCall) (string, bool, *cursorpb.ToolCall, bool) {
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return err.Error(), true, nil, true
	}
	step, _ := a.str("current_step", "currentStep")
	final, _ := a.str("final_summary", "finalSummary")
	subtitle, _ := a.str("completed_subtitle", "completedSubtitle")
	ui := &cursorpb.ToolCall{
		ToolCallId: &call.ID,
		Tool: &cursorpb.ToolCall_CommunicateUpdateToolCall{CommunicateUpdateToolCall: &cursorpb.CommunicateUpdateToolCall{
			Args: &cursorpb.CommunicateUpdateArgs{CurrentStep: &step, FinalSummary: &final, CompletedSubtitle: &subtitle},
			Result: &cursorpb.CommunicateUpdateResult{Result: &cursorpb.CommunicateUpdateResult_Success{Success: &cursorpb.CommunicateUpdateSuccess{
				CurrentStep: step,
			}}},
		}},
	}
	text := step
	if final != "" {
		text = final
	}
	if text == "" {
		return "UpdateCurrentStep requires current_step", true, ui, true
	}
	return text, false, ui, true
}

func finishTodos(call provider.ToolCall) (string, bool, *cursorpb.ToolCall, bool) {
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return err.Error(), true, nil, true
	}
	raw, _ := a["todos"].([]any)
	if raw == nil {
		return "TodoWrite requires todos", true, todoUI(call.ID, nil, false), true
	}
	merge, _ := a["merge"].(bool)
	now := time.Now().UnixMilli()
	items := make([]*cursorpb.TodoItem, 0, len(raw))
	var lines []string
	for _, item := range raw {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		todo := &cursorpb.TodoItem{
			Id:        fmt.Sprint(obj["id"]),
			Content:   fmt.Sprint(obj["content"]),
			Status:    todoStatus(fmt.Sprint(obj["status"])),
			CreatedAt: now,
			UpdatedAt: now,
		}
		items = append(items, todo)
		lines = append(lines, fmt.Sprintf("- [%s] %s", strings.TrimPrefix(todo.Status.String(), "TODO_STATUS_"), todo.Content))
	}
	text := "todos updated\n" + strings.Join(lines, "\n")
	return text, false, todoUI(call.ID, items, merge), true
}

func todoUI(id string, items []*cursorpb.TodoItem, merge bool) *cursorpb.ToolCall {
	return &cursorpb.ToolCall{
		ToolCallId: &id,
		Tool: &cursorpb.ToolCall_UpdateTodosToolCall{UpdateTodosToolCall: &cursorpb.UpdateTodosToolCall{
			Args: &cursorpb.UpdateTodosArgs{Todos: items, Merge: merge},
			Result: &cursorpb.UpdateTodosResult{Result: &cursorpb.UpdateTodosResult_Success{Success: &cursorpb.UpdateTodosSuccess{
				Todos:      items,
				TotalCount: int32(len(items)),
				WasMerge:   merge,
			}}},
		}},
	}
}

func todoStatus(value string) cursorpb.TodoStatus {
	switch strings.ToLower(strings.ReplaceAll(value, "-", "_")) {
	case "in_progress":
		return cursorpb.TodoStatus_TODO_STATUS_IN_PROGRESS
	case "completed":
		return cursorpb.TodoStatus_TODO_STATUS_COMPLETED
	case "cancelled", "canceled":
		return cursorpb.TodoStatus_TODO_STATUS_CANCELLED
	default:
		return cursorpb.TodoStatus_TODO_STATUS_PENDING
	}
}
