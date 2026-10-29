package agent

import (
	"context"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
)

// Route 是一条 BidiAppend 的分流结论。
type Route struct {
	RequestID string
	ModelID   string
	Local     bool
}

type slot struct {
	session *Session
	decided bool
	ready   chan struct{}
	born    time.Time
}

type finished struct {
	session *Session
	at      time.Time
}

// Hub 按 request_id 把 BidiAppend 的客户端消息路由到对应的 Session。
type Hub struct {
	mu       sync.Mutex
	slots    map[string]*slot
	finished map[string]*finished
	// byConv 按会话记最近一条答完的 run。客户端 resume 时经常换一个新的 request id
	// （不带 X-Original-Request-Id），只按 id 找会把这条当成新的一轮，客户端拿不到 checkpoint。
	byConv  map[string]*finished
	lookup  func(id string) (config.Model, bool)
	history *History
}

func New(lookup func(id string) (config.Model, bool), history *History) *Hub {
	return &Hub{slots: map[string]*slot{}, finished: map[string]*finished{}, byConv: map[string]*finished{}, lookup: lookup, history: history}
}

// Bidi 处理一条已解压的 BidiAppend 请求体。
func (h *Hub) Bidi(body []byte) (Route, error) {
	requestID, raw, err := protox.DecodeBidi(body)
	if err != nil {
		return Route{}, err
	}
	var msg cursorpb.AgentClientMessage
	// 不做“读一部分”的挽救：读不成 AgentClientMessage 的，多半根本不是 agent 回合
	// （例如 Cursor 3.26 本地循环的 InferenceClientMessage），硬认成本地回合会把请求吞掉，客户端一直等。
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return Route{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	route, _, _ := h.applyLocked(requestID, &msg)
	return route, nil
}

// Client 处理 Agents 窗口 WebSocket 里的一条客户端消息。
// start 只在这一次新建了本地会话时为真，调用方据此开始 Run。
func (h *Hub) Client(requestID string, msg *cursorpb.AgentClientMessage) (Route, *Session, bool) {
	if msg == nil || requestID == "" {
		return Route{}, nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	return h.applyLocked(requestID, msg)
}

func (h *Hub) applyLocked(requestID string, msg *cursorpb.AgentClientMessage) (Route, *Session, bool) {
	s := h.slotLocked(requestID)
	route := Route{RequestID: requestID}
	start := false
	delivered := false
	switch m := msg.GetMessage().(type) {
	case *cursorpb.AgentClientMessage_RunRequest:
		route.ModelID = modelID(m.RunRequest)
		// 同一条 run 的 resume（回合结束后约 90 秒客户端会重发）：接到已经答完的会话上，
		// 不再新建会话调一次模型。新建会得到不同的输出，把用户已经看到的思考和回答覆盖掉。
		// 这一条不能要求 run request 里带模型：resume 的请求常常不带 requested_model，
		// 按模型判归属会把它当成官方的 run 交给官方，客户端只能报 Error resuming chat。
		if s.session == nil {
			if f := h.finished[requestID]; f != nil {
				s.session = f.session
				// resume 可能带着新的动作（继续运行、后台任务完成）。沿用旧动作会把上一轮的
				// 用户消息再插一遍，接着跑的那一轮就多了一条重复的输入。
				s.session.adopt(m.RunRequest)
			} else if disk := h.sessionFromDiskLocked(requestID); disk != nil {
				s.session = disk
				s.session.adopt(m.RunRequest)
			}
		}
		if s.session == nil && resumeOnly(m.RunRequest.GetAction()) {
			// resume 常常换一个新的 request id（不总带 X-Original-Request-Id）。按会话找到
			// 最近答完的那条 run 回放，否则会被当成新的一轮：模型一看历史已完整就 turn_skip，
			// 客户端什么 checkpoint 都拿不到，弹 Error resuming chat。
			if conv := m.RunRequest.GetConversationId(); conv != "" {
				if f := h.byConv[conv]; f != nil && !f.session.Running() {
					s.session = f.session
					// 和同 id 的 resume 一样接住新动作，否则继续跑的那一轮会沿用上一轮的用户消息。
					s.session.adopt(m.RunRequest)
				}
			}
		}
		// 模型可能不在这条 run 的第一条消息里。先到的消息认不出，不能让后面带自定义模型的消息改走官方。
		if s.session == nil && route.ModelID != "" && h.lookup != nil {
			if model, ok := h.lookup(route.ModelID); ok {
				s.session = newSession(requestID, model, m.RunRequest, h.history)
				// 同一会话开了新的一轮：之前答完的那条 run 已经过时，换 id 的 resume 不能再回放它，
				// 否则客户端会拿到上一轮的检查点，把这一轮的内容盖掉。新一轮答完时 Done 会重新登记。
				if conv := m.RunRequest.GetConversationId(); conv != "" {
					delete(h.byConv, conv)
				}
				for _, id := range model.Fallback {
					if fb, ok := h.lookup(id); ok && fb.ID != model.ID {
						s.session.Fallbacks = append(s.session.Fallbacks, fb)
					}
				}
				start = true
			}
		}
		// NoteResume 会先把已答完的会话挂到新 request id 上，却来不及 adopt。
		// 若不接住新动作，接着跑时仍用上一轮的 UserMessageAction，等于把旧指令再插一遍。
		if s.session != nil && !start && !s.session.Running() {
			s.session.adopt(m.RunRequest)
		}
		if !s.decided {
			s.decided = true
			close(s.ready)
		}
	case *cursorpb.AgentClientMessage_ExecClientMessage:
		if s.session != nil {
			s.session.deliver(m.ExecClientMessage)
		}
	case *cursorpb.AgentClientMessage_InteractionResponse:
		if s.session != nil {
			s.session.deliverInteraction(m.InteractionResponse)
		}
	case *cursorpb.AgentClientMessage_ConversationAction:
		session := s.session
		if session == nil {
			if id := actionRunID(m.ConversationAction); id != "" {
				if other := h.slots[id]; other != nil {
					session = other.session
				}
			}
		}
		if session == nil {
			// 后台任务完成、订阅通知这类动作会送到已经答完的 run 上（客户端认为这条 run 还活着），
			// 不会另起一条 run。不认识它就只剩两条路：交给官方（自定义模型会拿到 401），
			// 或在本地丢掉，客户端都会报 Error resuming chat。留到下一次 resume 时接着处理。
			if f := h.finished[requestID]; f != nil {
				session = f.session
			} else {
				session = h.sessionFromDiskLocked(requestID)
			}
		}
		if session != nil {
			session.deliverAction(m.ConversationAction)
			delivered = true
		}
	case *cursorpb.AgentClientMessage_KvClientMessage:
		if s.session != nil {
			s.session.deliverKV(m.KvClientMessage)
		}
	}
	route.Local = s.session != nil || delivered
	return route, s.session, start
}

func actionRunID(action *cursorpb.ConversationAction) string {
	if action == nil {
		return ""
	}
	return action.GetInjectContextAction().GetExpectedRunId()
}

// Wait 等 BidiAppend 判定完成，返回本地 Session；走官方时返回 nil。
func (h *Hub) Wait(ctx context.Context, requestID string) (*Session, error) {
	h.mu.Lock()
	s := h.slotLocked(requestID)
	ready := s.ready
	h.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return s.session, nil
}

// Active 是正在本进程里跑的自定义模型回合数。
// 只看“正在跑”，不看槽位里挂着的历史会话：Cursor 会把 Agents 的 WebSocket 留在池里，
// 那一轮早就答完了，槽位却还在。以前按槽位计数，热替换和自更新会一直以为有回合在跑，
// 等满 12 分钟都等不到空闲（2026-10-20 的实际现象）。
func (h *Hub) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.slots {
		if s.session != nil && s.session.Running() {
			n++
		}
	}
	return n
}

func (h *Hub) Done(requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.slots[requestID]
	delete(h.slots, requestID)
	if s != nil && s.session != nil && s.session.Finished() {
		// 已答完的 run 保留一段时间：客户端会在回合结束后再用同一条 run 发 resume，
		// 或是带 X-Original-Request-Id 重新开一条请求。见 resumeFinished / runSSE 的恢复路径。
		f := &finished{session: s.session, at: time.Now()}
		h.finished[requestID] = f
		if conv := s.session.ConversationID(); conv != "" {
			h.byConv[conv] = f
			// 热交接会丢掉内存 finished；把 request→会话 落到磁盘，resume 才能回放。
			if h.history != nil {
				if err := h.history.SaveRun(requestID, conv); err != nil {
					slog.Debug("SaveRun failed", "request", requestID, "error", err)
				}
			}
		}
	}
}

// resumeOnly 表示这条 run request 只是让未完成的运行继续，不会带新的用户输入。
// 只有这种请求才按会话去认领已经答完的 run；带用户消息的必须当成新的一轮。
func resumeOnly(action *cursorpb.ConversationAction) bool {
	return action.GetResumeAction() != nil || action.GetGoalContinuationAction() != nil
}

// Finished 报告 requestID 是不是本机已经答完的一条 run。
func (h *Hub) Finished(requestID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.finished[requestID]
	return f != nil
}

// Owns 报告 requestID 是不是本机的会话（正在本地回答，或本地已经答完）。
// 本机代为回答的官方请求（心跳、resume、握手）必须先用它确认归属；官方的 run 一律原样转发。
func (h *Hub) Owns(requestID string) bool {
	if requestID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished[requestID] != nil {
		return true
	}
	s := h.slots[requestID]
	if s != nil && s.session != nil {
		return true
	}
	// 热交接后心跳仍会带旧 request id；磁盘有 run 记录就认本机，避免转官方 401。
	return h.history != nil && h.history.LoadRun(requestID) != ""
}

// KnowsConversation 报告这个 conversation id 是不是本机答过或正在答的自定义模型会话。
// 用于改名同步（UpdateConversationMetadata）：本机会话回空成功，官方会话照常转发。
func (h *Hub) KnowsConversation(conversationID string) bool {
	if conversationID == "" || h == nil {
		return false
	}
	h.mu.Lock()
	if h.byConv[conversationID] != nil {
		h.mu.Unlock()
		return true
	}
	for _, s := range h.slots {
		if s != nil && s.session != nil && s.session.ConversationID() == conversationID {
			h.mu.Unlock()
			return true
		}
	}
	for _, f := range h.finished {
		if f != nil && f.session != nil && f.session.ConversationID() == conversationID {
			h.mu.Unlock()
			return true
		}
	}
	hist := h.history
	h.mu.Unlock()
	return hist != nil && hist.LoadState(conversationID) != nil
}

// sessionFromDiskLocked 用磁盘上的 request→会话 索引和 checkpoint 重建一条已答完的会话。
// 调用方必须已持有 h.mu。
func (h *Hub) sessionFromDiskLocked(requestID string) *Session {
	if h.history == nil || requestID == "" {
		return nil
	}
	conv := h.history.LoadRun(requestID)
	if conv == "" {
		return nil
	}
	state := h.history.LoadState(conv)
	if state == nil {
		return nil
	}
	run := &cursorpb.AgentRunRequest{ConversationId: &conv}
	sess := newSession(requestID, config.Model{}, run, h.history)
	sess.baseState = state
	sess.finished.Store(true)
	f := &finished{session: sess, at: time.Now()}
	h.finished[requestID] = f
	h.byConv[conv] = f
	return sess
}

// NoteResume 把一条带 X-Original-Request-Id 的 resume 接到 oldID 已经答完的会话上。
// 返回 false 表示这条 original 不是本机答完的 run，调用方按原流程处理。
func (h *Hub) NoteResume(oldID, newID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var sess *Session
	if f := h.finished[oldID]; f != nil {
		sess = f.session
	} else if s := h.slots[oldID]; s != nil && s.session != nil && s.session.Finished() {
		// WS 连接还开在池里时 serveLocalAgent 还没走到 Done，会话留在 slots 里。
		sess = s.session
	} else if sess = h.sessionFromDiskLocked(oldID); sess != nil {
		// 热交接/重启后内存 finished 已空：按磁盘 run 索引重建，避免 Error resuming chat。
	}
	if sess == nil {
		return false
	}
	if newID == "" {
		newID = oldID
	}
	// RunSSE 常和 BidiAppend resume 几乎同时到。Wait 会先 slotLocked 出一个空槽并卡在 ready 上；
	// 这里如果整槽替换，Wait 仍等旧 channel，30 秒后超时把自定义模型 resume 转给官方，
	// 客户端就弹 Error resuming chat（2026-10-22 17F: request 56924b58 / 47530dec）。
	if s := h.slots[newID]; s != nil {
		s.session = sess
		s.decided = true
		select {
		case <-s.ready:
		default:
			close(s.ready)
		}
		return true
	}
	ready := make(chan struct{})
	close(ready)
	h.slots[newID] = &slot{session: sess, decided: true, ready: ready, born: time.Now()}
	return true
}

func (h *Hub) slotLocked(id string) *slot {
	s, ok := h.slots[id]
	if !ok {
		s = &slot{ready: make(chan struct{}), born: time.Now()}
		h.slots[id] = s
	}
	return s
}

func (h *Hub) sweepLocked() {
	for id, s := range h.slots {
		if time.Since(s.born) > 6*time.Hour {
			delete(h.slots, id)
		}
	}
	for id, f := range h.finished {
		if time.Since(f.at) > 2*time.Hour {
			delete(h.finished, id)
		}
	}
	for conv, f := range h.byConv {
		if time.Since(f.at) > 2*time.Hour {
			delete(h.byConv, conv)
		}
	}
}

func modelID(run *cursorpb.AgentRunRequest) string {
	id := run.GetRequestedModel().GetModelId()
	if id == "" {
		id = run.GetModelDetails().GetModelId()
	}
	if i := strings.IndexByte(id, '['); i > 0 {
		id = id[:i]
	}
	return id
}
