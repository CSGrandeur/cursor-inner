package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

// errBreak 表示用户用一条新的用户消息打断正在执行的工具。当前工具中止，同一轮用新消息继续。
// InjectContextAction 里的用户消息是 steer：当前工具做完，消息并进下一次模型调用。
var errBreak = errors.New("user message interrupted the tool")

type steer struct {
	kind        string
	text        string
	user        *cursorpb.UserMessage
	ctx         *cursorpb.RequestContext
	injectionID string
}

func (s *Session) deliverAction(action *cursorpb.ConversationAction) {
	if action == nil {
		return
	}
	if action.GetCancelAction() != nil {
		slog.Debug("对话动作", "request", s.RequestID, "kind", "cancel")
		s.Stop()
		return
	}
	var item steer
	switch {
	case action.GetUserMessageAction() != nil:
		message := action.GetUserMessageAction()
		item = steer{kind: "break", user: message.GetUserMessage(), ctx: message.GetRequestContext()}
	case action.GetInjectContextAction() != nil:
		inject := action.GetInjectContextAction()
		if inject.GetExpectedRunId() != "" && inject.GetExpectedRunId() != s.RequestID {
			slog.Debug("对话动作", "request", s.RequestID, "kind", "inject-reject", "expected", inject.GetExpectedRunId())
			s.emit(injectionRejected(inject.GetInjectionId(), "expected run mismatch"))
			return
		}
		if user := inject.GetUserContext().GetUserMessage(); user.GetText() != "" {
			item = steer{kind: "steer", user: user, injectionID: inject.GetInjectionId()}
			break
		}
		text := actionText(action)
		if text == "" {
			return
		}
		item = steer{kind: "insert", text: text, injectionID: inject.GetInjectionId()}
	case action.GetSummarizeAction() != nil:
		item = steer{kind: "summarize"}
	default:
		text := actionText(action)
		if text == "" {
			return
		}
		item = steer{kind: "insert", text: text}
	}
	slog.Debug("对话动作", "request", s.RequestID, "kind", item.kind)
	if item.injectionID != "" {
		s.emit(injectionQueued(item.injectionID))
	}
	select {
	case s.steer <- item:
	default:
	}
}

func actionText(action *cursorpb.ConversationAction) string {
	if inject := action.GetInjectContextAction(); inject != nil {
		if user := inject.GetUserContext(); user.GetUserMessage().GetText() != "" {
			return user.GetUserMessage().GetText()
		}
		if sys := inject.GetSystemContext(); sys.GetContent() != "" {
			return sys.GetContent()
		}
	}
	if tasks := action.GetBackgroundTaskCompletionAction(); tasks != nil {
		var lines []string
		for _, item := range tasks.GetCompletions() {
			line := strings.TrimSpace(item.GetTitle() + " " + item.GetDetail())
			if line != "" {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

func (s *Session) hold(item steer) {
	switch item.kind {
	case "insert":
		s.inserts = append(s.inserts, item.text)
	case "steer":
		s.steers = append(s.steers, item)
	case "summarize":
		s.wantSummary = true
	case "break":
		s.breakUser = item.user
		s.breakCtx = item.ctx
	}
}

func (s *Session) turnContext() *cursorpb.RequestContext {
	if s.breakCtx != nil {
		return s.breakCtx
	}
	return s.reqCtx
}

func (s *Session) drainSteer(send Emit, messages []provider.Message) ([]provider.Message, bool, error) {
	for {
		select {
		case item := <-s.steer:
			s.hold(item)
		default:
			goto drained
		}
	}
drained:
	steered := false
	for _, text := range s.inserts {
		messages = append(messages, provider.Message{Role: "user", Content: text})
		if err := send(appended(text)); err != nil {
			return messages, steered, err
		}
	}
	s.inserts = nil
	for _, item := range s.steers {
		steered = true
		messages = append(messages, userTurn(item.user, s.reqCtx))
		if err := send(appendedUser(item.user)); err != nil {
			return messages, steered, err
		}
		if item.injectionID != "" {
			if err := send(injectionDelivered(item.injectionID)); err != nil {
				return messages, steered, err
			}
		}
	}
	s.steers = nil
	if s.breakUser != nil {
		steered = true
		messages = append(messages, userTurn(s.breakUser, s.turnContext()))
		if err := send(appendedUser(s.breakUser)); err != nil {
			return messages, steered, err
		}
		s.breakUser = nil
		s.breakCtx = nil
	}
	return messages, steered, nil
}

func (s *Session) emit(msg *cursorpb.AgentServerMessage) {
	s.notifyMu.Lock()
	notify := s.notify
	s.notifyMu.Unlock()
	if notify != nil && msg != nil {
		_ = notify(msg)
	}
}

func injectionQueued(id string) *cursorpb.AgentServerMessage {
	return interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ContextInjectionState{ContextInjectionState: &cursorpb.ContextInjectionStateUpdate{
		InjectionId: id,
		State: &cursorpb.ContextInjectionState{State: &cursorpb.ContextInjectionState_Queued{
			Queued: &cursorpb.ContextInjectionQueued{},
		}},
	}}})
}

func injectionRejected(id, reason string) *cursorpb.AgentServerMessage {
	return interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ContextInjectionState{ContextInjectionState: &cursorpb.ContextInjectionStateUpdate{
		InjectionId: id,
		State: &cursorpb.ContextInjectionState{State: &cursorpb.ContextInjectionState_Rejected{
			Rejected: &cursorpb.ContextInjectionRejected{Reason: reason},
		}},
	}}})
}

func appended(text string) *cursorpb.AgentServerMessage {
	return appendedUser(&cursorpb.UserMessage{Text: text})
}

func appendedUser(user *cursorpb.UserMessage) *cursorpb.AgentServerMessage {
	if user == nil {
		user = &cursorpb.UserMessage{}
	}
	return interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_UserMessageAppended{UserMessageAppended: &cursorpb.UserMessageAppendedUpdate{
		UserMessage: user,
	}}})
}

func injectionDelivered(id string) *cursorpb.AgentServerMessage {
	return interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ContextInjectionState{ContextInjectionState: &cursorpb.ContextInjectionStateUpdate{
		InjectionId: id,
		State: &cursorpb.ContextInjectionState{State: &cursorpb.ContextInjectionState_Delivered{
			Delivered: &cursorpb.ContextInjectionDelivered{DeliveredAtMs: time.Now().UnixMilli()},
		}},
	}}})
}

func (s *Session) summarize(ctx context.Context, send Emit, model config.Model, dial dialer.Func, messages []provider.Message) ([]provider.Message, error) {
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_SummaryStarted{SummaryStarted: &cursorpb.SummaryStartedUpdate{}}})); err != nil {
		return messages, err
	}
	prompt := append(append([]provider.Message{}, messages...), provider.Message{Role: "user", Content: "Summarize the conversation above as specified. Reply with the summary only."})
	_, reply, err := chatOrCompact(ctx, model, dial, "You are compacting conversation history for future model turns. Produce a concise plain-text summary that preserves durable context. Do not address the user.", prompt, nil, func(text string) error {
		return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_Summary{Summary: &cursorpb.SummaryUpdate{Summary: text}}}))
	}, nil)
	if err != nil {
		return messages, err
	}
	text := strings.TrimSpace(reply.Content)
	s.summaryText = text
	s.promptTokens = reply.PromptTokens
	s.completionTokens = reply.CompletionTokens
	s.cacheTokens = reply.CacheTokens
	out := append([]provider.Message{{Role: "user", Content: "<summary>\n" + text + "\n</summary>"}}, recentTail(messages, 2)...)
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_SummaryCompleted{SummaryCompleted: &cursorpb.SummaryCompletedUpdate{HookMessage: &text}}})); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Session) checkpoint(send Emit, conversation, system string, messages []provider.Message, summary string, mode cursorpb.AgentMode) error {
	if conversation == "" {
		return nil
	}
	if summary != "" {
		s.summaries++
	}
	window := s.Model.ContextWindow
	var prevBlobs map[string][]byte
	if s.history != nil {
		prevBlobs = s.history.Blobs()
	}
	built, err := writeCheckpoint(system, s.RequestID, messages, mode, window, estimateTokens(messages), summary, s.summaries, s.seenBlobs, s.baseState, prevBlobs)
	if err != nil {
		return err
	}
	if s.seenBlobs == nil {
		s.seenBlobs = map[string]struct{}{}
	}
	for _, blob := range built.Blobs {
		if err := s.history.PutBlob(blob.ID, blob.Data); err != nil {
			return err
		}
		s.seenBlobs[string(blob.ID)] = struct{}{}
		s.nextID++
		if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_KvServerMessage{KvServerMessage: &cursorpb.KvServerMessage{
			Id:      s.nextID,
			Message: &cursorpb.KvServerMessage_SetBlobArgs{SetBlobArgs: &cursorpb.SetBlobArgs{BlobId: blob.ID, BlobData: blob.Data}},
		}}}); err != nil {
			return err
		}
	}
	if err := s.history.SaveState(conversation, built.State); err != nil {
		return err
	}
	s.baseState = built.State
	return send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ConversationCheckpointUpdate{ConversationCheckpointUpdate: built.State}})
}
