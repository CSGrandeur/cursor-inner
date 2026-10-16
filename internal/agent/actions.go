package agent

import (
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// 一次运行由 AgentRunRequest.action 发起。每种动作在 Cursor 协议里含义不同：
// 用户消息、计划、通知、后台任务结果是新的输入；ResumeAction 只是让未完成的运行继续；
// 取消、转后台等动作不需要模型回应。这里按动作含义决定给模型加什么输入、是否需要调用模型。

// turnPlan 是对本次动作的解读。
type turnPlan struct {
	kind      string
	ctx       *cursorpb.RequestContext
	user      *cursorpb.UserMessage   // 本次的用户消息（按用户回合包装）
	prepend   []*cursorpb.UserMessage // 用户消息前面排队的消息
	extraUser []*cursorpb.UserMessage // 额外按用户回合加入的消息（通知等）
	inputs    []string                // 以普通文本加入的输入（后台任务结果、计划等）
	mode      cursorpb.AgentMode
	summarize bool
	// resume 为 true 时没有新输入，只在上一轮没做完时才调用模型。
	resume bool
	// skip 非空时本次不调用模型，内容是原因。
	skip string
}

const (
	maxPlanText   = 64 << 10
	maxAnswerText = 8 << 10
)

func planTurn(action *cursorpb.ConversationAction) turnPlan {
	p := turnPlan{kind: actionName(action)}
	switch {
	case action.GetUserMessageAction() != nil:
		a := action.GetUserMessageAction()
		p.ctx, p.user, p.prepend = a.GetRequestContext(), a.GetUserMessage(), a.GetPrependUserMessages()
		p.mode = p.user.GetMode()
		if p.user == nil {
			p.skip = "用户消息动作里没有消息"
		}
	case action.GetSummarizeAction() != nil:
		p.summarize = true
	case action.GetResumeAction() != nil:
		p.ctx = action.GetResumeAction().GetRequestContext()
		p.resume = true
	case action.GetGoalContinuationAction() != nil:
		// 协议里这个动作没有字段，含义是继续推进当前目标，按继续运行处理。
		p.resume = true
	case action.GetBackgroundTaskCompletionAction() != nil:
		if text := completionText(action.GetBackgroundTaskCompletionAction().GetCompletions(), true); text != "" {
			p.inputs = append(p.inputs, text)
		} else {
			p.skip = "后台任务只有进度通知，没有完成的结果"
		}
	case action.GetSubscriptionNotificationAction() != nil:
		a := action.GetSubscriptionNotificationAction()
		p.ctx = a.GetRequestContext()
		for _, n := range a.GetNotifications() {
			if strings.TrimSpace(n.GetText()) != "" {
				p.extraUser = append(p.extraUser, n)
			}
		}
		if len(p.extraUser) == 0 {
			p.skip = "订阅通知没有内容"
		}
	case action.GetStartPlanAction() != nil:
		a := action.GetStartPlanAction()
		p.ctx, p.user = a.GetRequestContext(), a.GetUserMessage()
		p.mode = p.user.GetMode()
		if p.user == nil {
			p.skip = "开始计划动作里没有消息"
		}
	case action.GetExecutePlanAction() != nil:
		a := action.GetExecutePlanAction()
		p.ctx, p.mode = a.GetRequestContext(), a.GetExecutionMode()
		p.inputs = append(p.inputs, planText(a))
	case action.GetAsyncAskQuestionCompletionAction() != nil:
		p.inputs = append(p.inputs, answerText(action.GetAsyncAskQuestionCompletionAction()))
	case action.GetInjectContextAction() != nil:
		a := action.GetInjectContextAction()
		if u := a.GetUserContext(); strings.TrimSpace(u.GetUserMessage().GetText()) != "" {
			p.ctx, p.user = u.GetRequestContext(), u.GetUserMessage()
			p.mode = p.user.GetMode()
		} else if c := strings.TrimSpace(a.GetSystemContext().GetContent()); c != "" {
			p.inputs = append(p.inputs, c)
		} else {
			p.skip = "注入的上下文是空的"
		}
	case action.GetCancelAction() != nil:
		p.skip = "取消动作不需要模型回应"
	case action.GetCancelSubagentAction() != nil:
		p.skip = "取消子代理不需要模型回应"
	case action.GetBackgroundShellAction() != nil, action.GetBackgroundSubagentAction() != nil:
		p.skip = "转入后台只针对正在运行的工具，新运行里没有要转的工具"
	case action.GetShellCommandAction() != nil:
		p.skip = "用户直接运行的命令不需要模型回应"
	default:
		p.skip = "没有可识别的动作"
	}
	return p
}

// unfinished 判断历史末尾是否还有没做完的工作：工具结果待处理、用户消息待回复、回复被截断或工具调用没有结果。
func unfinished(messages []provider.Message) (bool, string) {
	if len(messages) == 0 {
		return false, "没有历史，也没有新输入"
	}
	last := messages[len(messages)-1]
	switch {
	case last.Role == "tool":
		return true, ""
	case last.Role == "user":
		return true, ""
	case last.Role == "assistant" && len(last.ToolCalls) > 0:
		return true, ""
	case last.Role == "assistant" && last.Truncated:
		return true, ""
	}
	return false, "上一轮已经完整结束，没有新的输入需要回应"
}

func taskEnum(name string, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// completionText 把已完成的后台任务整理成一段结构化输入，只收 TASK_FINISHED，进度通知跳过。
func completionText(items []*cursorpb.BackgroundTaskCompletion, logIt bool) string {
	var b strings.Builder
	n := 0
	for _, c := range items {
		kind := taskEnum(c.GetKind().String(), "BACKGROUND_TASK_KIND_")
		reason := taskEnum(c.GetReason().String(), "BACKGROUND_TASK_COMPLETION_REASON_")
		status := taskEnum(c.GetStatus().String(), "BACKGROUND_TASK_STATUS_")
		if logIt {
			slog.Info("background_completion 后台任务通知", "kind", kind, "reason", reason, "status", status, "task_id", truncID(c.GetTaskId()))
		}
		if c.GetReason() != cursorpb.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED {
			continue
		}
		n++
		fmt.Fprintf(&b, "- kind: %s\n  status: %s\n  task_id: %s\n", kind, status, c.GetTaskId())
		if t := strings.TrimSpace(c.GetTitle()); t != "" {
			fmt.Fprintf(&b, "  title: %s\n", t)
		}
		for _, kv := range [][2]string{{"output_path", c.GetOutputPath()}, {"subagent_id", c.GetSubagentId()}, {"tool_call_id", c.GetToolCallId()}, {"thread_id", c.GetThreadId()}} {
			if kv[1] != "" {
				fmt.Fprintf(&b, "  %s: %s\n", kv[0], kv[1])
			}
		}
		if d := strings.TrimSpace(c.GetDetail()); d != "" {
			fmt.Fprintf(&b, "  detail: |\n    %s\n", strings.ReplaceAll(d, "\n", "\n    "))
		}
	}
	if n == 0 {
		return ""
	}
	return "<background_task_completions>\nThese background tasks have finished. Review the results and continue if anything needs follow-up.\n" + b.String() + "</background_task_completions>"
}

func truncID(s string) string {
	if len(s) > diagMaxID {
		return s[:diagMaxID]
	}
	return s
}

func clip(s string, max int) string {
	if len(s) > max {
		return s[:max] + "\n…(truncated)"
	}
	return s
}

func jsonOf(m proto.Message, max int) string {
	if m == nil {
		return ""
	}
	raw, err := protojson.Marshal(m)
	if err != nil {
		return ""
	}
	return clip(string(raw), max)
}

func planText(a *cursorpb.ExecutePlanAction) string {
	var b strings.Builder
	b.WriteString("<execute_plan>\nThe user approved this plan. Carry it out now.\n")
	for _, kv := range [][2]string{{"plan_id", a.GetPlanId()}, {"plan_file_path", a.GetPlanFilePath()}, {"plan_file_uri", a.GetPlanFileUri()}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}
	if c := a.GetPlanFileContent(); c != "" {
		b.WriteString("<plan>\n" + clip(c, maxPlanText) + "\n</plan>\n")
	} else if j := jsonOf(a.GetPlan(), maxPlanText); j != "" {
		b.WriteString("<plan>\n" + j + "\n</plan>\n")
	}
	b.WriteString("</execute_plan>")
	return b.String()
}

func answerText(a *cursorpb.AsyncAskQuestionCompletionAction) string {
	var b strings.Builder
	b.WriteString("<ask_question_answer>\n")
	if id := a.GetOriginalToolCallId(); id != "" {
		fmt.Fprintf(&b, "tool_call_id: %s\n", id)
	}
	if j := jsonOf(a.GetOriginalArgs(), maxAnswerText); j != "" {
		b.WriteString("question: " + j + "\n")
	}
	if j := jsonOf(a.GetResult(), maxAnswerText); j != "" {
		b.WriteString("answer: " + j + "\n")
	}
	b.WriteString("</ask_question_answer>")
	return b.String()
}

// needsModel 表示这个动作可能需要调用模型并带上工具。
func (p turnPlan) needsModel() bool {
	return p.skip == "" && !p.summarize
}

// apply 把本次动作的输入接到历史后面，返回新的历史；skip 非空时表示本次不调用模型。
func (p turnPlan) apply(messages []provider.Message, ctx *cursorpb.RequestContext) ([]provider.Message, string) {
	if p.skip != "" || p.summarize {
		return messages, p.skip
	}
	if p.user != nil {
		messages = prependUsers(messages, p.prepend)
		messages = append(messages, userTurn(p.user, ctx))
	}
	for _, u := range p.extraUser {
		messages = append(messages, userTurn(u, ctx))
	}
	for _, text := range p.inputs {
		messages = append(messages, provider.Message{Role: "user", Content: text})
	}
	if p.resume {
		if ok, why := unfinished(messages); !ok {
			return messages, why
		}
	}
	return messages, ""
}
