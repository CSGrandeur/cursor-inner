package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"

	"google.golang.org/protobuf/proto"
)

const (
	maxSteps          = 50
	parallelLimit     = 8
	argFailLimit      = 3
	heartbeatInterval = 5 * time.Second
	contextTimeout    = 15 * time.Second
)

// Session 是一次由自定义模型回答的 Agent 运行，从 BidiAppend 的 run_request 开始，到 RunSSE 流结束为止。
type Session struct {
	RequestID        string
	Model            config.Model
	Web              dialer.Func // 联网工具（WebFetch、WebSearch、出图）按全局出站代理拨号
	run              *cursorpb.AgentRunRequest
	history          *History
	inbox            chan *cursorpb.ExecClientMessage
	replies          chan *cursorpb.InteractionResponse
	stopped          chan struct{}
	nextID           uint32
	inflight         []uint32
	argFails         map[string]int
	terminals        string
	workspaces       []string
	dial             dialer.Func
	mcp              []*cursorpb.McpToolDefinition
	early            map[uint32]*cursorpb.ExecClientMessage
	steer            chan steer
	inserts          []string
	breakUser        *cursorpb.UserMessage
	wantSummary      bool
	autoSummarized   bool
	toolCount        int
	toolUses         map[string]int
	toolOrder        []string
	promptTokens     int
	completionTokens int
	cacheTokens      int
	seenBlobs        map[string]struct{}
	baseState        *cursorpb.ConversationStateStructure
	kv               chan *cursorpb.KvClientMessage
	summaries        uint32
	summaryText      string
	notifyMu         sync.Mutex
	notify           Emit
}

func newSession(requestID string, model config.Model, run *cursorpb.AgentRunRequest, history *History) *Session {
	return &Session{
		RequestID: requestID,
		Model:     model,
		run:       run,
		history:   history,
		inbox:     make(chan *cursorpb.ExecClientMessage, 64),
		replies:   make(chan *cursorpb.InteractionResponse, 8),
		kv:        make(chan *cursorpb.KvClientMessage, 8),
		stopped:   make(chan struct{}, 1),
		argFails:  map[string]int{},
		early:     map[uint32]*cursorpb.ExecClientMessage{},
		steer:     make(chan steer, 8),
	}
}

func (s *Session) deliverKV(msg *cursorpb.KvClientMessage) {
	if msg == nil {
		return
	}
	select {
	case s.kv <- msg:
	default:
	}
}

func (s *Session) restoreFromRequest(ctx context.Context, send Emit, conversation string) ([]provider.Message, error) {
	state := s.run.GetConversationState()
	if state == nil || (len(state.GetTurns()) == 0 && len(state.GetRootPromptMessagesJson()) == 0) {
		return nil, nil
	}
	blobs, err := importPrefetched(s.run.GetPreFetchedBlobs())
	if err != nil {
		return nil, err
	}
	if s.seenBlobs == nil {
		s.seenBlobs = map[string]struct{}{}
	}
	for id, data := range blobs {
		s.seenBlobs[id] = struct{}{}
		if err := s.history.PutBlob([]byte(id), data); err != nil {
			return nil, err
		}
	}
	missing := missingBlobs(state, func(id []byte) ([]byte, bool) {
		if data, ok := blobs[string(id)]; ok {
			return data, true
		}
		return s.history.GetBlob(id)
	})
	for _, id := range missing {
		data, err := s.fetchBlob(ctx, send, id)
		if err != nil {
			return nil, err
		}
		if data == nil {
			continue
		}
		blobs[string(id)] = data
	}
	for id, data := range s.history.Blobs() {
		if _, ok := blobs[id]; !ok {
			blobs[id] = data
		}
		s.seenBlobs[id] = struct{}{}
	}
	s.baseState = state
	return restoreMessages(state, blobs)
}

func missingBlobs(state *cursorpb.ConversationStateStructure, get func([]byte) ([]byte, bool)) [][]byte {
	seen := map[string]struct{}{}
	var out [][]byte
	need := func(id []byte) {
		if len(id) == 0 {
			return
		}
		if _, ok := seen[string(id)]; ok {
			return
		}
		seen[string(id)] = struct{}{}
		if _, ok := get(id); !ok {
			out = append(out, append([]byte(nil), id...))
		}
	}
	for _, id := range state.GetTurns() {
		need(id)
		data, ok := get(id)
		if !ok {
			continue
		}
		var turn cursorpb.ConversationTurnStructure
		if proto.Unmarshal(data, &turn) != nil || turn.GetAgentConversationTurn() == nil {
			continue
		}
		need(turn.GetAgentConversationTurn().GetUserMessage())
		for _, step := range turn.GetAgentConversationTurn().GetSteps() {
			need(step)
		}
	}
	for _, id := range state.GetRootPromptMessagesJson() {
		need(id)
	}
	return out
}

func (s *Session) fetchBlob(ctx context.Context, send Emit, id []byte) ([]byte, error) {
	if data, ok := s.history.GetBlob(id); ok {
		return data, nil
	}
	s.nextID++
	want := s.nextID
	if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_KvServerMessage{KvServerMessage: &cursorpb.KvServerMessage{
		Id:      want,
		Message: &cursorpb.KvServerMessage_GetBlobArgs{GetBlobArgs: &cursorpb.GetBlobArgs{BlobId: id}},
	}}}); err != nil {
		return nil, err
	}
	for {
		select {
		case msg := <-s.kv:
			if msg.GetId() != want {
				continue
			}
			data := msg.GetGetBlobResult().GetBlobData()
			if len(data) > 0 {
				_ = s.history.PutBlob(id, data)
			}
			return data, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *Session) noteTool(name string) {
	slog.Debug("调用工具", "conversation", s.RequestID, "tool", name)
	s.toolCount++
	if s.toolUses == nil {
		s.toolUses = map[string]int{}
	}
	if s.toolUses[name] == 0 {
		s.toolOrder = append(s.toolOrder, name)
	}
	s.toolUses[name]++
}

func formatToolUses(order []string, counts map[string]int) string {
	var b strings.Builder
	for _, name := range order {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s×%d", name, counts[name])
	}
	return b.String()
}

func (s *Session) deliver(msg *cursorpb.ExecClientMessage) {
	select {
	case s.inbox <- msg:
	default:
		slog.Warn(fmt.Sprintf("会话 %s 的执行结果队列已满，丢弃 id=%d", s.RequestID, msg.GetId()))
	}
}

func (s *Session) deliverInteraction(msg *cursorpb.InteractionResponse) {
	select {
	case s.replies <- msg:
	default:
		slog.Warn(fmt.Sprintf("会话 %s 的交互回复队列已满，丢弃 id=%d", s.RequestID, msg.GetId()))
	}
}

// Stop 取消这一轮，并让挂起的工具收到 abort。
func (s *Session) Stop() {
	select {
	case s.stopped <- struct{}{}:
	default:
	}
}

// Emit 把一条服务端消息写进 RunSSE 流，需要可以被多个 goroutine 调用。
type Emit func(*cursorpb.AgentServerMessage) error

// Run 执行完整的模型与工具循环，返回后由调用方写结束帧。
func (s *Session) Run(ctx context.Context, dial dialer.Func, emit Emit) (runErr error) {
	s.dial = dial
	turnStarted := time.Now()
	var mu sync.Mutex
	send := func(m *cursorpb.AgentServerMessage) error {
		mu.Lock()
		defer mu.Unlock()
		return emit(m)
	}
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	s.notifyMu.Lock()
	s.notify = send
	s.notifyMu.Unlock()
	defer func() {
		s.notifyMu.Lock()
		s.notify = nil
		s.notifyMu.Unlock()
	}()
	go func() {
		select {
		case <-s.stopped:
			cancelRun()
		case <-ctx.Done():
		}
	}()
	stop := make(chan struct{})
	var beats sync.WaitGroup
	beats.Add(1)
	defer func() {
		close(stop)
		beats.Wait()
	}()
	go func() {
		defer beats.Done()
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_Heartbeat{Heartbeat: &cursorpb.HeartbeatUpdate{}}}))
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	action := s.run.GetAction().GetUserMessageAction()
	reqCtx := action.GetRequestContext()
	if reqCtx.GetEnv() == nil && action != nil {
		if fetched, err := s.requestContext(ctx, send); err == nil {
			reqCtx = fetched
		} else {
			slog.Warn(fmt.Sprintf("会话 %s 没有拿到运行环境：%v", s.RequestID, err))
		}
	}
	conversation := s.run.GetConversationId()
	messages := s.history.Load(conversation)
	if restored, err := s.restoreFromRequest(ctx, send, conversation); err != nil {
		return err
	} else if restored != nil {
		messages = restored
	}
	summarizeOnly := s.run.GetAction().GetSummarizeAction() != nil
	user := action.GetUserMessage()
	if !summarizeOnly && user != nil {
		messages = append(messages, userTurn(user))
	}
	s.terminals = reqCtx.GetEnv().GetTerminalsFolder()
	s.workspaces = append([]string{}, reqCtx.GetEnv().GetWorkspacePaths()...)
	for _, folder := range user.GetSelectedContext().GetFolders() {
		if folder.GetPath() != "" {
			s.workspaces = append(s.workspaces, folder.GetPath())
		}
	}
	s.mcp = tools.MCPDefs(reqCtx)
	slog.Debug("mcp 工具", "count", len(s.mcp))
	mode := user.GetMode()
	system := systemPrompt(s.Model.DisplayName, reqCtx) + mcpNote(s.mcp) + modeNote(mode, reqCtx) + s.roleNote()
	model := s.Model
	params := turnParams(s.run)
	if model.Reasoning && params.Effort != "" {
		model.Effort = params.Effort
	}
	if model.FastSupport && model.Type == "openai-chat" {
		model.Fast = params.Fast
	}
	model.ContextWindow = effectiveWindow(model.ContextWindow, params.ContextTokens)
	s.Model = model
	catalog := s.catalogFor(mode)
	name := s.Model.DisplayName
	if name == "" {
		name = s.Model.ID
	}
	short := conversationID(s.run.GetConversationId())
	slog.Info("▶ "+name, "conversation", short, "turn", userTurns(messages), "window", model.ContextWindow)
	defer func() {
		seconds := time.Since(turnStarted).Round(time.Millisecond).Seconds()
		uses := formatToolUses(s.toolOrder, s.toolUses)
		if runErr != nil {
			slog.Error("✗ "+name, "conversation", short, "seconds", seconds, "reason", provider.Explain(runErr), "error", runErr)
			return
		}
		slog.Info("✓ "+name, "conversation", short, "seconds", seconds, "tools", s.toolCount, "tool_uses", uses, "prompt_tokens", s.promptTokens, "completion_tokens", s.completionTokens, "cache_tokens", s.cacheTokens)
	}()

	defer func() {
		for _, id := range s.inflight {
			_ = send(abortExec(id))
		}
		messages = provider.CloseDangling(messages)
		if err := s.history.Save(conversation, messages); err != nil && conversation != "" {
			slog.Error(fmt.Sprintf("会话 %s 的历史没有保存：%v", s.RequestID, err))
		}
	}()
	if summarizeOnly {
		var err error
		messages, err = s.summarize(ctx, send, model, dial, messages)
		if err != nil {
			return err
		}
		if err := s.checkpoint(send, conversation, system, messages, s.summaryText, mode); err != nil {
			return err
		}
		return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
	}
next:
	for step := 0; step < maxSteps; step++ {
		var err error
		messages, err = s.drainSteer(send, messages)
		if err != nil {
			return err
		}
		if s.wantSummary {
			s.wantSummary = false
			messages, err = s.summarize(ctx, send, model, dial, messages)
			if err != nil {
				return err
			}
		}
		thought := false
		started := time.Now()
		messages = compactMessages(messages, model.ContextWindow)
		if !s.autoSummarized && overBudget(messages, model.ContextWindow) {
			s.autoSummarized = true
			slog.Debug("自动压缩", "conversation", short, "window", model.ContextWindow)
			messages, err = s.summarize(ctx, send, model, dial, messages)
			if err != nil {
				return err
			}
		}
		var reply provider.Message
		messages, reply, err = chatOrCompact(ctx, model, dial, system, messages, catalog, func(text string) error {
			return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: text}}}))
		}, func(text string) error {
			thought = true
			return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ThinkingDelta{ThinkingDelta: &cursorpb.ThinkingDeltaUpdate{Text: text}}}))
		})
		if err != nil {
			return err
		}
		if thought {
			ms := int32(min(time.Since(started).Milliseconds(), 1<<31-1))
			if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ThinkingCompleted{ThinkingCompleted: &cursorpb.ThinkingCompletedUpdate{ThinkingDurationMs: ms}}})); err != nil {
				return err
			}
		}
		s.promptTokens = reply.PromptTokens
		s.completionTokens = reply.CompletionTokens
		s.cacheTokens = reply.CacheTokens
		messages = append(messages, reply)
		for i := 0; i < len(reply.ToolCalls); {
			end := i + 1
			if tools.ReadOnly(reply.ToolCalls[i].Name) {
				for end < len(reply.ToolCalls) && end-i < parallelLimit && tools.ReadOnly(reply.ToolCalls[end].Name) {
					end++
				}
			}
			batch := reply.ToolCalls[i:end]
			i = end
			var results []toolOutcome
			var err error
			if len(batch) == 1 {
				outcome, runErr := s.runTool(ctx, send, batch[0])
				results = []toolOutcome{outcome}
				err = runErr
				if errors.Is(runErr, errBreak) {
					for _, call := range reply.ToolCalls[i-1:] {
						messages = append(messages, provider.Message{Role: "tool", ToolCallID: call.ID, Content: "Interrupted because the user sent a new message.", IsError: true})
					}
					if s.breakUser != nil {
						messages = append(messages, userTurn(s.breakUser))
						if err := send(appended(s.breakUser.GetText())); err != nil {
							return err
						}
						s.breakUser = nil
					}
					continue next
				}
			} else {
				results, err = s.runParallel(ctx, send, batch)
			}
			for _, outcome := range results {
				if outcome.call.ID == "" {
					continue
				}
				messages = append(messages, toolResult(outcome))
				if outcome.mode != "" {
					mode = modeFromID(outcome.mode)
					catalog = s.catalogFor(mode)
					system = systemPrompt(s.Model.DisplayName, reqCtx) + mcpNote(s.mcp) + modeNote(mode, reqCtx) + s.roleNote()
				}
			}
			if err != nil {
				return err
			}
			for _, outcome := range results {
				if outcome.stop {
					return fmt.Errorf("工具 %s 连续 %d 次参数错误，已停止", outcome.call.Name, argFailLimit)
				}
			}
		}
		if reply.Truncated {
			messages = append(messages, provider.Message{Role: "user", Content: "Your output was truncated. Finish the work in smaller steps."})
			continue
		}
		if len(reply.ToolCalls) == 0 {
			if err := s.checkpoint(send, conversation, system, messages, s.summaryText, mode); err != nil {
				return err
			}
			return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
		}
	}
	return fmt.Errorf("模型连续调用工具超过 %d 步，已停止", maxSteps)
}

type toolOutcome struct {
	call  provider.ToolCall
	text  string
	isErr bool
	stop  bool
	mode  string
	card  *cursorpb.ToolCall
}

func toolResult(outcome toolOutcome) provider.Message {
	var card *cursorpb.ToolCall
	if outcome.card != nil {
		card = proto.Clone(outcome.card).(*cursorpb.ToolCall)
	}
	return provider.Message{Role: "tool", ToolCallID: outcome.call.ID, Content: tools.ClipForModel(outcome.text), IsError: outcome.isErr, Card: card}
}

func (s *Session) catalogFor(mode cursorpb.AgentMode) []provider.Tool {
	base := toolCatalog(mode, tools.Catalog())
	if kind := s.run.GetSubagentTypeName(); kind != "" {
		base = subagentCatalog(kind, tools.Catalog())
	}
	catalog := append(base, tools.MCPCatalog(s.mcp)...)
	if s.Model.ImageBaseURL == "" {
		catalog = withoutTool(catalog, "GenerateImage")
	}
	return catalog
}

func (s *Session) runTool(ctx context.Context, send Emit, call provider.ToolCall) (toolOutcome, error) {
	call = s.repair(call)
	if text, isErr, ui, ok := tools.FinishLocal(call); ok {
		return s.card(send, call, text, isErr, ui, "")
	}
	if text, isErr, ui, ok := tools.ListMCP(call, s.mcp); ok {
		return s.card(send, call, text, isErr, ui, "")
	}
	if _, _, _, ok := tools.SearchRequest(call); ok {
		return s.semSearch(ctx, send, call)
	}
	if query, ui, err, ok := tools.BeginInteraction(call); ok {
		if err != nil {
			return s.argFailure(call, err), nil
		}
		s.nextID++
		query.Id = s.nextID
		return s.ask(ctx, send, call, query, ui)
	}
	if def, ok := tools.MatchMCP(call, s.mcp); ok {
		s.nextID++
		exec, ui, pending, err := tools.MCPExec(s.nextID, call, def)
		if err != nil {
			return s.argFailure(call, err), nil
		}
		return s.execLoop(ctx, send, call, exec, ui, pending)
	}
	s.nextID++
	exec, ui, pending, err := tools.Request(s.nextID, call)
	if err != nil {
		slog.Debug(fmt.Sprintf("会话 %s 工具 %s 无法执行：%v", s.RequestID, call.Name, err))
		return s.argFailure(call, err), nil
	}
	if err := s.prepareTask(exec, pending); err != nil {
		return s.card(send, call, err.Error(), true, ui, "")
	}
	return s.execLoop(ctx, send, call, exec, ui, pending)
}

func (s *Session) prepareTask(exec *cursorpb.ExecServerMessage, pending *tools.Pending) error {
	sub := exec.GetSubagentArgs()
	if sub == nil || s.run == nil {
		return nil
	}
	parent := s.run.GetRequestedModel().GetModelId()
	for _, ov := range s.run.GetSubagentModelOverrides() {
		if ov.GetSubagentType() != "" && ov.GetSubagentType() != sub.GetSubagentType() {
			continue
		}
		if ov.GetDisabled() {
			return fmt.Errorf("subagent %s is disabled", sub.GetSubagentType())
		}
		if ov.GetInherit() {
			sub.ModelId = parent
		}
		if model := ov.GetModel(); model.GetModelId() != "" {
			sub.ModelId = model.GetModelId()
		}
	}
	if sub.ModelId == "" {
		sub.ModelId = parent
	}
	if sub.GetEnvironment() == cursorpb.SubagentExecutionEnvironment_SUBAGENT_EXECUTION_ENVIRONMENT_CLOUD && parent != "" {
		sub.Environment = cursorpb.SubagentExecutionEnvironment_SUBAGENT_EXECUTION_ENVIRONMENT_LOCAL
		if pending != nil {
			pending.Note = "This subagent runs locally because the parent model is not reachable from Cursor cloud."
		}
	}
	return nil
}

func (s *Session) card(send Emit, call provider.ToolCall, text string, isErr bool, ui *cursorpb.ToolCall, mode string) (toolOutcome, error) {
	if ui != nil {
		if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
			return toolOutcome{}, err
		}
		if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
			return toolOutcome{}, err
		}
	}
	if isErr {
		out := s.argFailure(call, fmt.Errorf("%s", text))
		out.card = ui
		return out, nil
	}
	s.argFails[call.Name] = 0
	return toolOutcome{call: call, text: text, mode: mode, card: ui}, nil
}

func (s *Session) ask(ctx context.Context, send Emit, call provider.ToolCall, query *cursorpb.InteractionQuery, ui *cursorpb.ToolCall) (toolOutcome, error) {
	s.noteTool(call.Name)
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
		return toolOutcome{}, err
	}
	if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionQuery{InteractionQuery: query}}); err != nil {
		return toolOutcome{}, err
	}
	resp, err := s.waitReply(ctx, query.GetId())
	if err != nil {
		return toolOutcome{}, err
	}
	text, isErr, mode := tools.CompleteInteraction(ui, resp, tools.ImageAPI{BaseURL: s.Model.ImageBaseURL, APIKey: s.Model.ImageAPIKey, Model: s.Model.ImageModel}, s.Web)
	if !isErr {
		if err := s.writeGeneratedImage(ctx, send, ui); err != nil {
			text += "\nFailed to write the image file: " + err.Error()
		}
	}
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
		return toolOutcome{}, err
	}
	if isErr {
		s.argFails[call.Name] = 0
		return toolOutcome{call: call, text: text, isErr: true, mode: mode, card: ui}, nil
	}
	s.argFails[call.Name] = 0
	return toolOutcome{call: call, text: text, mode: mode, card: ui}, nil
}

func userTurns(messages []provider.Message) int {
	n := 0
	for _, message := range messages {
		if message.Role == "user" {
			n++
		}
	}
	return n
}

func conversationID(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}

func (s *Session) writeGeneratedImage(ctx context.Context, send Emit, ui *cursorpb.ToolCall) error {
	tool := ui.GetGenerateImageToolCall()
	if tool == nil || tool.GetResult().GetSuccess() == nil {
		return nil
	}
	path := tool.GetArgs().GetFilePath()
	data := tool.GetResult().GetSuccess().GetImageData()
	if path == "" || data == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	s.nextID++
	exec := &cursorpb.ExecServerMessage{
		Id:     s.nextID,
		ExecId: fmt.Sprintf("image-%d", s.nextID),
		Message: &cursorpb.ExecServerMessage_WriteArgs{WriteArgs: &cursorpb.WriteArgs{
			Path: path, FileBytes: raw, ToolCallId: ui.GetToolCallId(),
		}},
	}
	if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: exec}}); err != nil {
		return err
	}
	s.track(exec.GetId())
	_, err = s.await(ctx, exec.GetId())
	s.untrack(exec.GetId())
	return err
}

func (s *Session) waitReply(ctx context.Context, id uint32) (*cursorpb.InteractionResponse, error) {
	for {
		select {
		case resp := <-s.replies:
			if resp.GetId() != id || resp.GetResult() == nil {
				continue
			}
			return resp, nil
		case <-s.stopped:
			return nil, fmt.Errorf("the turn was cancelled")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *Session) execLoop(ctx context.Context, send Emit, call provider.ToolCall, exec *cursorpb.ExecServerMessage, ui *cursorpb.ToolCall, pending *tools.Pending) (toolOutcome, error) {
	s.argFails[call.Name] = 0
	s.prepareExec(pending, exec)
	s.noteTool(call.Name)
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
		return toolOutcome{}, err
	}
	for {
		if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: exec}}); err != nil {
			return toolOutcome{}, err
		}
		s.track(exec.GetId())
		for {
			result, err := s.await(ctx, exec.GetId())
			if errors.Is(err, errBreak) {
				s.untrack(exec.GetId())
				_ = send(abortExec(exec.GetId()))
				return toolOutcome{}, err
			}
			if err != nil {
				return toolOutcome{}, err
			}
			next, deltas, text, isErr, done := pending.Feed(result, ui)
			for _, delta := range deltas {
				if err := send(delta); err != nil {
					return toolOutcome{}, err
				}
			}
			if done {
				s.untrack(exec.GetId())
				if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
					return toolOutcome{}, err
				}
				return toolOutcome{call: call, text: text, isErr: isErr, card: ui}, nil
			}
			if next != nil {
				s.untrack(exec.GetId())
				s.nextID++
				next.Id = s.nextID
				next.ExecId = fmt.Sprintf("%s-%d", call.ID, s.nextID)
				exec = next
				break
			}
		}
	}
}

func (s *Session) prepareExec(pending *tools.Pending, exec *cursorpb.ExecServerMessage) {
	pending.Terminals = s.terminals
	if shell := exec.GetShellStreamArgs(); shell != nil && s.run.GetConversationId() != "" {
		id := s.run.GetConversationId()
		shell.ConversationId = &id
	}
	if sub := exec.GetSubagentArgs(); sub != nil && s.run.GetConversationId() != "" {
		id := s.run.GetConversationId()
		sub.ParentConversationId = &id
		if sub.RootParentConversationId == nil {
			sub.RootParentConversationId = &id
		}
	}
}

func (s *Session) runParallel(ctx context.Context, send Emit, calls []provider.ToolCall) ([]toolOutcome, error) {
	outcomes := make([]toolOutcome, len(calls))
	waiting := map[uint32]int{}
	type live struct {
		pending *tools.Pending
		ui      *cursorpb.ToolCall
		call    provider.ToolCall
	}
	jobs := map[int]live{}
	for i, call := range calls {
		call = s.repair(call)
		s.nextID++
		exec, ui, pending, err := tools.Request(s.nextID, call)
		if err != nil {
			slog.Debug(fmt.Sprintf("会话 %s 工具 %s 无法执行：%v", s.RequestID, call.Name, err))
			outcomes[i] = s.argFailure(call, err)
			continue
		}
		s.argFails[call.Name] = 0
		slog.Debug("调用工具", "conversation", s.RequestID, "tool", call.Name)
		if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
			return outcomes, err
		}
		if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: exec}}); err != nil {
			return outcomes, err
		}
		s.track(exec.GetId())
		waiting[exec.GetId()] = i
		jobs[i] = live{pending: pending, ui: ui, call: call}
	}
	for len(waiting) > 0 {
		msg, err := s.awaitAny(ctx, waiting)
		if err != nil {
			return outcomes, err
		}
		index := waiting[msg.GetId()]
		delete(waiting, msg.GetId())
		s.untrack(msg.GetId())
		job := jobs[index]
		next, text, isErr := job.pending.Advance(msg, job.ui)
		if next != nil {
			s.nextID++
			next.Id = s.nextID
			next.ExecId = fmt.Sprintf("%s-%d", job.call.ID, s.nextID)
			if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: next}}); err != nil {
				return outcomes, err
			}
			s.track(next.GetId())
			waiting[next.GetId()] = index
			continue
		}
		if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: job.call.ID, ToolCall: job.ui}}})); err != nil {
			return outcomes, err
		}
		outcomes[index] = toolOutcome{call: job.call, text: text, isErr: isErr, card: job.ui}
	}
	return outcomes, nil
}

func (s *Session) repair(call provider.ToolCall) provider.ToolCall {
	fixed, ok := tools.RepairArguments(call.Arguments)
	if ok && fixed != call.Arguments {
		slog.Debug(fmt.Sprintf("会话 %s 修复了工具 %s 的参数", s.RequestID, call.Name))
		call.Arguments = fixed
	}
	return call
}

func (s *Session) argFailure(call provider.ToolCall, err error) toolOutcome {
	if s.argFails == nil {
		s.argFails = map[string]int{}
	}
	s.argFails[call.Name]++
	text := err.Error() + tools.SchemaHint(call.Name)
	return toolOutcome{call: call, text: text, isErr: true, stop: s.argFails[call.Name] >= argFailLimit}
}

func (s *Session) track(id uint32) {
	s.inflight = append(s.inflight, id)
}

func (s *Session) untrack(id uint32) {
	kept := s.inflight[:0]
	for _, have := range s.inflight {
		if have != id {
			kept = append(kept, have)
		}
	}
	s.inflight = kept
}

func (s *Session) awaitAny(ctx context.Context, waiting map[uint32]int) (*cursorpb.ExecClientMessage, error) {
	for id := range waiting {
		if msg, ok := s.early[id]; ok {
			delete(s.early, id)
			return msg, nil
		}
	}
	for {
		select {
		case msg := <-s.inbox:
			if _, ok := waiting[msg.GetId()]; ok {
				return msg, nil
			}
			s.early[msg.GetId()] = msg
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *Session) requestContext(ctx context.Context, send Emit) (*cursorpb.RequestContext, error) {
	s.nextID++
	id := s.nextID
	if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: &cursorpb.ExecServerMessage{
		Id:      id,
		ExecId:  fmt.Sprintf("request-context-%d", id),
		Message: &cursorpb.ExecServerMessage_RequestContextArgs{RequestContextArgs: &cursorpb.RequestContextArgs{}},
	}}}); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	msg, err := s.await(waitCtx, id)
	if err != nil {
		return nil, err
	}
	success := msg.GetRequestContextResult().GetSuccess()
	if success == nil {
		return nil, errors.New("Cursor 没有返回运行环境")
	}
	return success.GetRequestContext(), nil
}

// await 等待指定 id 的执行结果。先到的其他结果暂存，留给对应的等待者。
func (s *Session) await(ctx context.Context, id uint32) (*cursorpb.ExecClientMessage, error) {
	if msg, ok := s.early[id]; ok {
		delete(s.early, id)
		return msg, nil
	}
	for {
		select {
		case msg := <-s.inbox:
			if msg.GetId() == id {
				return msg, nil
			}
			s.early[msg.GetId()] = msg
		case item := <-s.steer:
			s.hold(item)
			if item.kind == "break" {
				return nil, errBreak
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func interaction(u *cursorpb.InteractionUpdate) *cursorpb.AgentServerMessage {
	return &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: u}}
}

func abortExec(id uint32) *cursorpb.AgentServerMessage {
	return &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerControlMessage{ExecServerControlMessage: &cursorpb.ExecServerControlMessage{
		Message: &cursorpb.ExecServerControlMessage_Abort{Abort: &cursorpb.ExecServerAbort{Id: id}},
	}}}
}
