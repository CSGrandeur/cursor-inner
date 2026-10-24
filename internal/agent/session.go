package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/runlog"
	"cursor-inner/internal/tools"

	"google.golang.org/protobuf/proto"
)

const (
	parallelLimit     = 8
	argFailLimit      = 3
	heartbeatInterval = 5 * time.Second
	contextTimeout    = 15 * time.Second
	toolStopped       = "The tool did not finish within the time limit and was stopped. Do not retry the same call. Use a different approach."
	shellLeftRunning  = "The shell client stopped reporting before the foreground wait ended. The command was not stopped. Check the terminal before starting it again."
	taskLeftRunning   = "The subagent did not return before the wait ended. It was not stopped. Continue without its result or resume it."
	replyStopped      = "The user did not respond in time. Continue without that answer."
	replyWait         = 10 * time.Minute
)

// maxSteps 是一轮里连续调用工具的上限。到上限不报错：告诉用户已暂停、发检查点并正常结束这一轮，
// 用户说“继续”就能接着做。原来 50 步就报错，长任务做着做着无声停下；200 步对长任务又太紧，
// Claude Code / Codex 这类交互式 agent 不按步数截断，只在用户按停止时结束。这里放到 1000，
// 真正的死循环交给 loopTracker，时间和成本用 turnBudget 兜底。测试可以改小。
var maxSteps = 1000

// turnBudget 是一轮最多跑多久。到点像步数到上限那样暂停并写检查点，用户说“继续”再开一轮。
// 按时间兜底比按步数更贴近长任务：干活的一轮可以跑很久，跑飞的一轮有时间上限。
var turnBudget = 45 * time.Minute

// stepLimitNotice 是到步数上限时给用户看的说明。
const stepLimitNotice = "\n\n(Paused after %d tool steps in this turn. The steps above are kept. Reply \"continue\" to keep going.)"

// turnBudgetNotice 是一轮跑满时间预算时给用户看的说明。
const turnBudgetNotice = "\n\n(Paused: this turn has been running for %d minutes (budget %d). The steps above are kept. Reply \"continue\" to keep going.)"

// loopStopNotice 是同一个工具调用连续重复到上限时给用户看的说明。
// 这里要正常收尾而不是报错：报错时客户端把整轮渲染成 Operation interrupted.，
// 已经流出来的思考、文件改动和回答都会跟着看不见。
// textLoopNotice 是正文复读被提前收尾时给用户看的说明（只发给界面，不进模型历史）。
const textLoopNotice = "\n\n(Stopped early: the reply began repeating the same text, so I ended it here. Reply \"continue\" to pick up from this point, or tell me what to change.)"

const loopStopNotice = "\n\n(Paused: the same tool call (%s) repeated %d times in a row, so I stopped before it looped further. The steps above are kept. Reply \"continue\" to keep going, or tell me what to change.)"

// silentToolLimit 是连续多少次工具调用客户端完全没回应后就结束这一轮。
// 客户端已放弃这次请求（例如它自己报了认证错误）时，继续发工具只会每次白等一个超时。
const silentToolLimit = 2

// silentToolNotice 是因客户端不回应工具而结束一轮时给用户看的说明。
const silentToolNotice = "\n\n(Stopped: the editor did not respond to %d tool calls in a row. Send a new message to retry.)"

// toolWaitLimit 是一次普通执行在没有新消息时最多再等多久。
// 到点就把失败交回模型，这一轮继续。测试可以改短。
// 命令行和子代理用 Pending.Wait，到点结束等待，不把还在跑的工作杀掉。
var toolWaitLimit = 60 * time.Second

// quietThinkingAfter 是模型调用还没有任何文字或思考时，先打开思考气泡的等待时间。
// Cursor 在生成中连续约 15 秒看不到文字、思考或进行中的工具，会显示 “Taking longer than expected”。
// 心跳不算。有的接口把整段流攒到结束才一次发出，这段空档会被当成卡住。
// 这里只给界面一个思考气泡，不写入回传给模型的思考内容。
var quietThinkingAfter = 2 * time.Second

// Session 是一次由自定义模型回答的 Agent 运行，从 BidiAppend 的 run_request 开始，到 RunSSE 流结束为止。
type Session struct {
	RequestID        string
	Model            config.Model
	Proxy            config.Proxy   // 出站代理设置，供备用模型按各自“走代理”开关拨号
	Fallbacks        []config.Model // 流式前失败时的备用模型
	Web              dialer.Func    // 联网工具（WebFetch、WebSearch、出图）按全局出站代理拨号
	run              *cursorpb.AgentRunRequest
	history          *History
	inbox            chan *cursorpb.ExecClientMessage
	replies          chan *cursorpb.InteractionResponse
	stopped          chan struct{}
	nextID           uint32
	inflight         []uint32
	argFails         map[string]int
	silentTools      int
	terminals        string
	workspaces       []string
	dial             dialer.Func
	mcp              []*cursorpb.McpToolDefinition
	early            map[uint32]*cursorpb.ExecClientMessage
	steer            chan struct{}
	queueMu          sync.Mutex
	queued           []steer
	inserts          []string
	pending          []steer
	reqCtx           *cursorpb.RequestContext
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
	finished         atomic.Bool
	running          atomic.Int32
	summaries        uint32
	summaryText      string
	notifyMu         sync.Mutex
	notify           Emit
	loops            *loopTracker
	editedPaths      []string
	fallbackUsed     []string
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
		steer:     make(chan struct{}, 1),
		loops:     newLoopTracker(),
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

// prependUsers 追加客户端补发的历史用户消息（如切换模型后把旧对话带过来）。
// 它们已经发生过，不能再套 userTurn 的包装：会重新打上本轮时间戳和 <user_query> 标签，
// 历史消息就变成带标签的新消息，写进 checkpoint 后客户端重建时显示一堆标签。
func prependUsers(messages []provider.Message, preps []*cursorpb.UserMessage) []provider.Message {
	for _, pre := range preps {
		if strings.TrimSpace(pre.GetText()) == "" && strings.TrimSpace(pre.GetSubagentSystemReminder()) == "" {
			continue
		}
		// History already happened: keep model-facing reminders in Content, never in Shown.
		shown := stripSystemReminders(pre.GetText())
		content := pre.GetText()
		if block := modelSystemReminder(pre.GetSubagentSystemReminder()); block != "" && !strings.Contains(content, block) {
			if content != "" {
				content = block + "\n\n" + content
			} else {
				content = block
			}
		}
		if strings.TrimSpace(shown) == "" && strings.TrimSpace(content) == "" {
			continue
		}
		messages = append(messages, provider.Message{Role: "user", Content: content, Shown: shown})
	}
	return messages
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

// ConversationID 是这条 run 所属的会话，用于按会话找回已经答完的 run。
func (s *Session) ConversationID() string {
	if s.run == nil {
		return ""
	}
	return s.run.GetConversationId()
}

// adopt 接住一条 resume 的 run request。会话已经答完，这时没有别的 goroutine 在读它。
// resume 偶尔不带会话 id 或模型，缺的沿用上一轮的，否则接着跑的那一轮会读不到历史。
func (s *Session) adopt(run *cursorpb.AgentRunRequest) {
	if run == nil || run.GetAction() == nil {
		return
	}
	if s.run != nil {
		if run.GetConversationId() == "" {
			run.ConversationId = s.run.ConversationId
		}
		if run.GetRequestedModel() == nil {
			run.RequestedModel = s.run.RequestedModel
		}
	}
	s.run = run
}

// HasQueuedInput 报告有没有还没并进历史的输入：后台任务结果、订阅通知、中途插入的消息。
// 这些输入可能在上一轮结束之后才到，客户端随后的 resume 就是来取它们的。
func (s *Session) HasQueuedInput() bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return len(s.queued) > 0 || len(s.inserts) > 0 || len(s.pending) > 0
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
// Finished 表示这一轮模型循环已经返回。之后同一条 run 上新开的连接是恢复，不是新的一轮。
func (s *Session) Finished() bool { return s.finished.Load() }

// Running 表示这一轮正在跑。热替换等“回合空闲”用这个判断，不看 Finished：
// 答完但连接还挂着的会话不算忙。
func (s *Session) Running() bool { return s.running.Load() > 0 }

// Replay 生成这一轮最终状态的完整回放：全部引用的 blob（SetBlobArgs）+ 最后一份 checkpoint。
// 客户端在回合结束后会 resume 同一条 run，用它确认“任务已完成、内容是什么”；
// 只回 turn_ended 的话客户端会报 Error resuming chat 并反复重试。
func (s *Session) Replay() ([]*cursorpb.AgentServerMessage, error) {
	state := s.baseState
	if state == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	var ids []string
	need := func(id []byte) {
		if len(id) == 0 {
			return
		}
		if _, ok := seen[string(id)]; ok {
			return
		}
		seen[string(id)] = struct{}{}
		ids = append(ids, string(id))
	}
	for _, id := range state.GetTurns() {
		need(id)
		if data, ok := s.history.GetBlob(id); ok {
			var turn cursorpb.ConversationTurnStructure
			if proto.Unmarshal(data, &turn) == nil && turn.GetAgentConversationTurn() != nil {
				need(turn.GetAgentConversationTurn().GetUserMessage())
				for _, step := range turn.GetAgentConversationTurn().GetSteps() {
					need(step)
				}
			}
		}
	}
	for _, id := range state.GetRootPromptMessagesJson() {
		need(id)
	}
	var out []*cursorpb.AgentServerMessage
	next := s.nextID
	for _, key := range ids {
		data, ok := s.history.GetBlob([]byte(key))
		if !ok {
			continue
		}
		next++
		out = append(out, &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_KvServerMessage{KvServerMessage: &cursorpb.KvServerMessage{
			Id:      next,
			Message: &cursorpb.KvServerMessage_SetBlobArgs{SetBlobArgs: &cursorpb.SetBlobArgs{BlobId: []byte(key), BlobData: data}},
		}}})
	}
	out = append(out, &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ConversationCheckpointUpdate{ConversationCheckpointUpdate: state}})
	return out, nil
}

func (s *Session) Run(ctx context.Context, dial dialer.Func, emit Emit) (runErr error) {
	s.running.Add(1)
	defer s.running.Add(-1)
	defer s.finished.Store(true)
	// 已经答完的 run 被 resume 时会在同一个会话上再跑一轮（例如后台任务完成），
	// 先清掉上一轮的结束标记和自动压缩记录，否则这一轮会被当成“已经结束”或被跳过压缩。
	s.finished.Store(false)
	s.autoSummarized = false
	s.dial = dial
	turnStarted := time.Now()
	var mu sync.Mutex
	send := func(m *cursorpb.AgentServerMessage) error {
		noteClient(s.RequestID, m)
		mu.Lock()
		defer mu.Unlock()
		err := emit(m)
		if err != nil {
			runlog.Emit(runlog.Note{Kind: "client", Request: s.RequestID, Detail: "write_error", Error: err.Error()})
		}
		return err
	}
	ctx, cancelRun := context.WithCancel(ctx)
	ctx = runlog.WithRequest(ctx, s.RequestID)
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

	plan := planTurn(s.run.GetAction())
	reqCtx := plan.ctx
	if reqCtx.GetEnv() == nil && plan.needsModel() {
		// 会调用模型的动作都需要和用户消息一样的运行环境，否则模式、工具和 MCP 都会缺。
		// 只做摘要或本来就不调用模型的动作不需要。
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
	summarizeOnly := plan.summarize
	user := plan.user
	messages, skipReason := plan.apply(messages, reqCtx)
	s.reqCtx = reqCtx
	s.terminals = reqCtx.GetEnv().GetTerminalsFolder()
	s.workspaces = append([]string{}, reqCtx.GetEnv().GetWorkspacePaths()...)
	for _, folder := range user.GetSelectedContext().GetFolders() {
		if folder.GetPath() != "" {
			s.workspaces = append(s.workspaces, folder.GetPath())
		}
	}
	s.mcp = tools.MCPDefs(reqCtx)
	slog.Debug("mcp 工具", "count", len(s.mcp))
	mode := plan.mode
	system := systemPrompt(s.Model.DisplayName, reqCtx) + mcpNote(s.mcp) + modeNote(mode, reqCtx) + s.roleNote() + familyNote(s.Model)
	model := s.Model
	if model.LastTest != nil && model.LastTest.Capabilities != nil && model.LastTest.Capabilities.TextToolMode {
		model.TextToolMode = true
	}
	params := turnParams(s.run)
	if model.Reasoning && params.Effort != "" {
		model.Effort = params.Effort
	}
	if model.FastSupport && model.Type == "openai-chat" {
		model.Fast = params.Fast
	}
	configured, requested := model.ContextWindow, params.ContextTokens
	model.ContextWindow = effectiveWindow(configured, requested)
	s.Model = model
	catalog := s.catalogFor(mode)
	name := s.Model.DisplayName
	if name == "" {
		name = s.Model.ID
	}
	short := conversationID(s.run.GetConversationId())
	if configured > 0 && requested > configured {
		// 菜单里不再给出超限档位，但旧客户端仍会带 200k 这类固定值。记下请求值和实际窗口，
		// 下次用户说“选了没生效”时能直接分辨是选择器没传过来，还是被模型上限截断了。
		slog.Info("context_clamped 请求的上下文超过模型上限，按模型上限处理",
			"conversation", short, "requested", requested, "configured", configured, "window", model.ContextWindow)
	}
	slog.Info("▶ "+name, "conversation", short, "turn", userTurns(messages), "window", model.ContextWindow)
	newTurnDiag(s.run, reqCtx, len(s.mcp), len(catalog), messages).log()
	if skipReason != "" && s.HasQueuedInput() {
		// 后台任务完成这类输入是在上一轮结束之后送进这条 run 的，动作本身还是“继续运行”，
		// 但确实有新的输入要交给模型。下一轮循环开头的 drainSteer 会把它并进历史。
		slog.Info("turn_continue 有新的输入，接着跑一轮", "conversation", short, "action", plan.kind, "reason", skipReason)
		skipReason = ""
	}
	if skipReason != "" {
		slog.Info("turn_skip 本次不调用模型", "conversation", short, "action", plan.kind, "reason", skipReason)
		return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
	}
	runlog.Emit(runlog.Note{Kind: "turn", Request: s.RequestID, Model: name, Detail: "start", Messages: userTurns(messages)})
	defer func() {
		seconds := time.Since(turnStarted).Round(time.Millisecond).Seconds()
		uses := formatToolUses(s.toolOrder, s.toolUses)
		if runErr != nil {
			slog.Error("✗ "+name, "conversation", short, "seconds", seconds, "reason", provider.Explain(runErr), "error", runErr)
			runlog.Emit(runlog.Note{Kind: "turn", Request: s.RequestID, Model: name, Detail: "error", ElapsedMs: time.Since(turnStarted).Milliseconds(), Tools: s.toolCount, Error: provider.Explain(runErr)})
			return
		}
		slog.Info("✓ "+name, "conversation", short, "seconds", seconds, "tools", s.toolCount, "tool_uses", uses, "prompt_tokens", s.promptTokens, "completion_tokens", s.completionTokens, "cache_tokens", s.cacheTokens)
		runlog.Emit(runlog.Note{Kind: "turn", Request: s.RequestID, Model: name, Detail: "end", ElapsedMs: time.Since(turnStarted).Milliseconds(), Tools: s.toolCount, Prompt: s.promptTokens, Output: s.completionTokens})
	}()

	sentCheckpoint := false
	defer func() {
		for _, id := range s.inflight {
			_ = send(abortExec(id))
		}
		messages = provider.CloseDangling(messages)
		if !sentCheckpoint && hasModelWork(messages) {
			if err := s.checkpoint(send, conversation, system, messages, s.summaryText, mode); err != nil {
				slog.Error(fmt.Sprintf("会话 %s 的检查点没有发出：%v", s.RequestID, err))
			}
		}
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
		sentCheckpoint = true
		return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
	}
next:
	for step := 0; step < maxSteps; step++ {
		if turnBudget > 0 {
			if elapsed := time.Since(turnStarted); elapsed >= turnBudget {
				slog.Debug("一轮跑满时间预算，暂停", "conversation", short, "elapsed_s", int(elapsed.Seconds()), "budget_s", int(turnBudget.Seconds()))
				if err := s.pauseTurn(send, conversation, system, messages, mode, fmt.Sprintf(turnBudgetNotice, int(elapsed.Minutes()), int(turnBudget.Minutes()))); err != nil {
					return err
				}
				sentCheckpoint = true
				return nil
			}
		}
		var err error
		messages, _, err = s.drainSteer(send, messages)
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
		var thought atomic.Bool
		started := time.Now()
		messages = compactMessages(messages, model.ContextWindow)
		messages = provider.EvictImages(messages, model)
		if !s.autoSummarized && overBudget(messages, model.ContextWindow) {
			s.autoSummarized = true
			slog.Debug("自动压缩", "conversation", short, "window", model.ContextWindow)
			messages, err = s.summarize(ctx, send, model, dial, messages)
			if err != nil {
				return err
			}
		}
		s.loops.resetText()
		stopQuiet, waitQuiet := startQuietThinking(ctx, send, &thought)
		var reply provider.Message
		repeated := false
		messages, model, reply, err = s.chatOrCompactSession(ctx, model, dial, system, messages, catalog, func(text string) error {
			stopQuiet()
			if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: text}}})); err != nil {
				return err
			}
			if s.loops.feedText(text) {
				repeated = true
				slog.Debug("正文复读，提前收尾本次生成", "conversation", short)
				return provider.ErrStopStream
			}
			return nil
		}, func(text string) error {
			thought.Store(true)
			stopQuiet()
			return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ThinkingDelta{ThinkingDelta: &cursorpb.ThinkingDeltaUpdate{Text: text}}}))
		})
		s.Model = model
		stopQuiet()
		waitQuiet()
		if thought.Load() {
			ms := int32(min(time.Since(started).Milliseconds(), 1<<31-1))
			if cerr := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ThinkingCompleted{ThinkingCompleted: &cursorpb.ThinkingCompletedUpdate{ThinkingDurationMs: ms}}})); cerr != nil && err == nil {
				err = cerr
			}
		}
		if err != nil {
			return err
		}
		if repeated {
			if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: textLoopNotice}}})); err != nil {
				return err
			}
		}
		s.promptTokens = reply.PromptTokens
		s.completionTokens = reply.CompletionTokens
		s.cacheTokens = reply.CacheTokens
		for i := range reply.ToolCalls {
			reply.ToolCalls[i] = tools.Canonical(reply.ToolCalls[i])
		}
		messages = append(messages, reply)
		if stop := s.loops.observe(reply.ToolCalls); stop {
			// 像步数到上限那样正常收尾，不要用错误结束这一轮。
			name := ""
			if len(reply.ToolCalls) > 0 {
				name = reply.ToolCalls[0].Name
			}
			slog.Debug("相同工具调用重复，暂停这一轮", "conversation", short, "tool", name, "limit", loopHardStopAt)
			if err := s.pauseTurn(send, conversation, system, messages, mode, fmt.Sprintf(loopStopNotice, name, loopHardStopAt)); err != nil {
				return err
			}
			sentCheckpoint = true
			return nil
		}
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
					messages, _, err = s.drainSteer(send, messages)
					if err != nil {
						return err
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
				if rem := s.loops.takeReminderFor(outcome.call.ID); rem != "" {
					outcome.text = appendLoopReminder(outcome.text, rem)
				}
				messages = append(messages, toolResult(outcome))
				if outcome.mode != "" {
					mode = modeFromID(outcome.mode)
					catalog = s.catalogFor(mode)
					system = systemPrompt(s.Model.DisplayName, reqCtx) + mcpNote(s.mcp) + modeNote(mode, reqCtx) + s.roleNote() + familyNote(s.Model)
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
			if s.silentTools >= silentToolLimit {
				slog.Debug("客户端连续不回应工具，结束这一轮", "conversation", short, "silent", s.silentTools)
				s.silentTools = 0
				if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: fmt.Sprintf(silentToolNotice, silentToolLimit)}}})); err != nil {
					return err
				}
				if err := s.checkpoint(send, conversation, system, messages, s.summaryText, mode); err != nil {
					return err
				}
				sentCheckpoint = true
				return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
			}
			var steered bool
			messages, steered, err = s.drainSteer(send, messages)
			if err != nil {
				return err
			}
			if steered {
				for _, call := range reply.ToolCalls[i:] {
					if call.ID == "" {
						continue
					}
					messages = append(messages, provider.Message{Role: "tool", ToolCallID: call.ID, Content: "Skipped because the user sent a new message.", IsError: true})
				}
				continue next
			}
		}
		if reply.Truncated {
			messages = append(messages, provider.Message{Role: "user", Content: "Your output was truncated. Finish the work in smaller steps."})
			continue
		}
		if len(reply.ToolCalls) == 0 {
			var steered bool
			messages, steered, err = s.drainSteer(send, messages)
			if err != nil {
				return err
			}
			if steered {
				continue
			}
			if err := s.checkpoint(send, conversation, system, messages, s.summaryText, mode); err != nil {
				return err
			}
			sentCheckpoint = true
			return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
		}
	}
	slog.Debug("一轮工具步数到上限，暂停", "conversation", short, "steps", maxSteps)
	if err := s.pauseTurn(send, conversation, system, messages, mode, fmt.Sprintf(stepLimitNotice, maxSteps)); err != nil {
		return err
	}
	sentCheckpoint = true
	return nil
}

// pauseTurn 正常结束这一轮并留下检查点。步数上限、时间预算和重复工具调用都走这里：
// 用错误结束的话，客户端会把整轮渲染成 Operation interrupted.，已经流出来的思考和改动都看不见。
func (s *Session) pauseTurn(send Emit, conversation, system string, messages []provider.Message, mode cursorpb.AgentMode, notice string) error {
	messages = provider.CloseDangling(messages)
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TextDelta{TextDelta: &cursorpb.TextDeltaUpdate{Text: notice}}})); err != nil {
		return err
	}
	if err := s.checkpoint(send, conversation, system, messages, s.summaryText, mode); err != nil {
		return err
	}
	return send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_TurnEnded{TurnEnded: &cursorpb.TurnEndedUpdate{}}}))
}

func hasModelWork(messages []provider.Message) bool {
	for _, message := range messages {
		if message.Role == "assistant" || message.Role == "tool" {
			return true
		}
	}
	return false
}

type toolOutcome struct {
	call   provider.ToolCall
	text   string
	images []provider.Image
	isErr  bool
	stop   bool
	mode   string
	card   *cursorpb.ToolCall
}

func toolResult(outcome toolOutcome) provider.Message {
	var card *cursorpb.ToolCall
	if outcome.card != nil {
		card = proto.Clone(outcome.card).(*cursorpb.ToolCall)
	}
	return provider.Message{Role: "tool", ToolCallID: outcome.call.ID, Content: tools.ClipForModel(outcome.text), Images: outcome.images, IsError: outcome.isErr, Card: card}
}

func (s *Session) catalogFor(mode cursorpb.AgentMode) []provider.Tool {
	base := toolCatalog(mode, tools.Catalog())
	if kind := s.run.GetSubagentTypeName(); kind != "" {
		base = subagentCatalog(kind, tools.Catalog())
	}
	// Merge 保证工具名唯一：自定义端点遇到重名工具会直接拒绝（Tool names must be unique）。
	catalog := tools.Merge(base, s.mcp)
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
		if errors.Is(err, tools.ErrBrowserFileURL) {
			return toolOutcome{call: call, text: err.Error(), isErr: true}, nil
		}
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
	s.noteEdited(call)
	return toolOutcome{call: call, text: text, mode: mode, card: ui}, nil
}

// noteEdited 记下这一轮改过的文件，压缩之后提醒模型先重读再改。
func (s *Session) noteEdited(call provider.ToolCall) {
	switch strings.ToLower(strings.ReplaceAll(call.Name, "_", "")) {
	case "strreplace", "write", "editnotebook":
	case "applypatch", "shell", "bash":
		s.editedPaths = uniquePaths(append(s.editedPaths, tools.PatchPaths(call)...))
		return
	default:
		return
	}
	var raw map[string]any
	if json.Unmarshal([]byte(call.Arguments), &raw) != nil {
		return
	}
	for _, key := range []string{"path", "file_path", "filePath", "target_notebook"} {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			s.editedPaths = uniquePaths(append(s.editedPaths, strings.TrimSpace(v)))
			return
		}
	}
}

func (s *Session) ask(ctx context.Context, send Emit, call provider.ToolCall, query *cursorpb.InteractionQuery, ui *cursorpb.ToolCall) (toolOutcome, error) {
	s.noteTool(call.Name)
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
		return toolOutcome{}, err
	}
	if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionQuery{InteractionQuery: query}}); err != nil {
		return toolOutcome{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, replyWait)
	resp, err := s.waitReply(waitCtx, query.GetId())
	cancel()
	if gaveUp(err, ctx) {
		slog.Debug("会话等待用户回答超时", "conversation", s.RequestID, "tool", call.Name)
		return s.stopTool(send, call, ui, replyStopped)
	}
	if errors.Is(err, errBreak) {
		slog.Info("等待用户回答时收到新消息，跳过提问继续这一轮", "conversation", s.RequestID, "tool", call.Name)
		_ = send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: call.ID, ToolCall: ui}}}))
		return toolOutcome{}, err
	}
	if err != nil {
		return toolOutcome{}, err
	}
	text, isErr, mode := tools.CompleteInteraction(ui, resp, tools.ImageAPI{BaseURL: s.Model.ImageBaseURL, APIKey: s.Model.ImageAPIKey, Model: s.Model.ImageModel}, s.Web)
	var images []provider.Image
	if image, ok := tools.GeneratedImage(ui); ok {
		images = []provider.Image{image}
	}
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
		return toolOutcome{call: call, text: text, images: images, isErr: true, mode: mode, card: ui}, nil
	}
	s.argFails[call.Name] = 0
	return toolOutcome{call: call, text: text, images: images, mode: mode, card: ui}, nil
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
	waitCtx, cancel := context.WithTimeout(ctx, toolWaitLimit)
	_, err = s.await(waitCtx, exec.GetId())
	cancel()
	s.untrack(exec.GetId())
	if gaveUp(err, ctx) {
		_ = send(abortExec(exec.GetId()))
		return errors.New("the file write did not finish within the time limit and was stopped")
	}
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
		case <-s.steer:
			if s.pullQueued() {
				return nil, errBreak
			}
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
	runlog.Emit(runlog.Note{Kind: "tool", Request: s.RequestID, Tool: call.Name, Detail: "start", WaitMs: toolBudget(pending).Milliseconds()})
	if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
		return toolOutcome{}, err
	}
	for {
		if err := send(&cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerMessage{ExecServerMessage: exec}}); err != nil {
			return toolOutcome{}, err
		}
		s.track(exec.GetId())
		started := time.Now()
		for {
			result, err := s.awaitTool(ctx, pending, exec.GetId())
			if gaveUp(err, ctx) {
				slog.Debug("会话工具等待超时", "conversation", s.RequestID, "tool", call.Name)
				s.untrack(exec.GetId())
				s.silentTools++
				if pending != nil && pending.Wait > 0 {
					runlog.Emit(runlog.Note{Kind: "tool", Request: s.RequestID, Tool: call.Name, Detail: "left_running", ElapsedMs: time.Since(started).Milliseconds()})
					return s.stopTool(send, call, ui, leftRunning(call.Name))
				}
				_ = send(abortExec(exec.GetId()))
				runlog.Emit(runlog.Note{Kind: "tool", Request: s.RequestID, Tool: call.Name, Detail: "stopped", ElapsedMs: time.Since(started).Milliseconds()})
				return s.stopTool(send, call, ui, toolStopped)
			}
			if errors.Is(err, errBreak) {
				s.untrack(exec.GetId())
				_ = send(abortExec(exec.GetId()))
				return toolOutcome{}, err
			}
			if err != nil {
				return toolOutcome{}, err
			}
			s.silentTools = 0
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
				runlog.Emit(runlog.Note{Kind: "tool", Request: s.RequestID, Tool: call.Name, Detail: "done", ElapsedMs: time.Since(started).Milliseconds(), Bytes: len(text)})
				if !isErr {
					s.noteEdited(call)
				}
				return toolOutcome{call: call, text: text, images: pending.Images, isErr: isErr, card: ui}, nil
			}
			if pastWait(pending, started) {
				slog.Debug("会话工具等待超时", "conversation", s.RequestID, "tool", call.Name)
				s.untrack(exec.GetId())
				runlog.Emit(runlog.Note{Kind: "tool", Request: s.RequestID, Tool: call.Name, Detail: "left_running", ElapsedMs: time.Since(started).Milliseconds()})
				return s.stopTool(send, call, ui, leftRunning(call.Name))
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
		waitCtx, cancel := context.WithTimeout(ctx, toolWaitLimit)
		msg, err := s.awaitAny(waitCtx, waiting)
		cancel()
		if gaveUp(err, ctx) {
			slog.Debug("会话并行工具等待超时", "conversation", s.RequestID, "pending", len(waiting))
			s.silentTools++
			runlog.Emit(runlog.Note{Kind: "tool", Request: s.RequestID, Detail: "parallel_stopped", Tools: len(waiting)})
			for id, index := range waiting {
				s.untrack(id)
				_ = send(abortExec(id))
				job := jobs[index]
				out, stopErr := s.stopTool(send, job.call, job.ui, toolStopped)
				if stopErr != nil {
					return outcomes, stopErr
				}
				outcomes[index] = out
			}
			return outcomes, nil
		}
		if err != nil {
			return outcomes, err
		}
		s.silentTools = 0
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
		outcomes[index] = toolOutcome{call: job.call, text: text, images: job.pending.Images, isErr: isErr, card: job.ui}
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

func (s *Session) awaitTool(ctx context.Context, pending *tools.Pending, id uint32) (*cursorpb.ExecClientMessage, error) {
	limit := toolWaitLimit
	if pending != nil && pending.Wait > 0 {
		limit = pending.Wait
	}
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return s.await(waitCtx, id)
}

func pastWait(pending *tools.Pending, started time.Time) bool {
	return pending != nil && pending.Wait > 0 && time.Since(started) >= pending.Wait
}

func leftRunning(name string) string {
	if name == "Task" {
		return taskLeftRunning
	}
	return shellLeftRunning
}

func gaveUp(err error, parent context.Context) bool {
	return errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil
}

func (s *Session) stopTool(send Emit, call provider.ToolCall, ui *cursorpb.ToolCall, text string) (toolOutcome, error) {
	if ui != nil {
		if err := send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: call.ID, ToolCall: ui}}})); err != nil {
			return toolOutcome{}, err
		}
	}
	return toolOutcome{call: call, text: text, isErr: true, card: ui}, nil
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
		case <-s.steer:
			if s.pullQueued() {
				return nil, errBreak
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// startQuietThinking 在安静超过 quietThinkingAfter 时发一条思考增量。
// stop 可多次调用。wait 等发送结束，避免思考增量排到本步结果之后。
func startQuietThinking(ctx context.Context, send Emit, thought *atomic.Bool) (stop func(), wait func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(quietThinkingAfter)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
		if !thought.CompareAndSwap(false, true) {
			return
		}
		_ = send(interaction(&cursorpb.InteractionUpdate{Message: &cursorpb.InteractionUpdate_ThinkingDelta{ThinkingDelta: &cursorpb.ThinkingDeltaUpdate{Text: " "}}}))
	}()
	var once sync.Once
	stop = func() { once.Do(func() { close(stopCh) }) }
	wait = func() { <-done }
	return stop, wait
}

func toolBudget(pending *tools.Pending) time.Duration {
	if pending != nil && pending.Wait > 0 {
		return pending.Wait
	}
	return toolWaitLimit
}

func noteClient(id string, m *cursorpb.AgentServerMessage) {
	if !runlog.Enabled() || m == nil {
		return
	}
	n := runlog.Note{Kind: "client", Request: id, Detail: "other"}
	switch msg := m.Message.(type) {
	case *cursorpb.AgentServerMessage_InteractionUpdate:
		switch u := msg.InteractionUpdate.GetMessage().(type) {
		case *cursorpb.InteractionUpdate_TextDelta:
			n.Detail = "text"
			n.Bytes = len(u.TextDelta.GetText())
		case *cursorpb.InteractionUpdate_ThinkingDelta:
			text := u.ThinkingDelta.GetText()
			n.Bytes = len(text)
			n.Detail = "thinking"
			if text == " " {
				n.Detail = "quiet_thinking"
			}
		case *cursorpb.InteractionUpdate_ThinkingCompleted:
			n.Detail = "thinking_done"
			n.ElapsedMs = int64(u.ThinkingCompleted.GetThinkingDurationMs())
		case *cursorpb.InteractionUpdate_Heartbeat:
			n.Detail = "heartbeat"
		case *cursorpb.InteractionUpdate_ToolCallStarted:
			n.Detail = "tool_started"
		case *cursorpb.InteractionUpdate_ToolCallCompleted:
			n.Detail = "tool_completed"
		case *cursorpb.InteractionUpdate_TurnEnded:
			n.Detail = "turn_ended"
		default:
			n.Detail = "interaction"
		}
	case *cursorpb.AgentServerMessage_ExecServerMessage:
		n.Detail = "exec"
	case *cursorpb.AgentServerMessage_ExecServerControlMessage:
		n.Detail = "exec_control"
	}
	now := time.Now()
	clientClock.Lock()
	if clientClock.last == nil {
		clientClock.last = map[string]time.Time{}
	}
	if prev, ok := clientClock.last[id]; ok {
		n.GapMs = now.Sub(prev).Milliseconds()
	}
	clientClock.last[id] = now
	clientClock.Unlock()
	runlog.Emit(n)
}

var clientClock struct {
	sync.Mutex
	last map[string]time.Time
}

func interaction(u *cursorpb.InteractionUpdate) *cursorpb.AgentServerMessage {
	return &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: u}}
}

func abortExec(id uint32) *cursorpb.AgentServerMessage {
	return &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_ExecServerControlMessage{ExecServerControlMessage: &cursorpb.ExecServerControlMessage{
		Message: &cursorpb.ExecServerControlMessage_Abort{Abort: &cursorpb.ExecServerAbort{Id: id}},
	}}}
}
