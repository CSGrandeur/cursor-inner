package agent

import (
	"context"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"
	"encoding/json"
	"google.golang.org/protobuf/proto"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// This private history journal is not a diagnostic log. Like conversation blobs,
// it contains user data and is owner-only. Dispatch is recorded BEFORE I/O: after
// an ambiguous disconnect, recovery must never retransmit a side-effecting exec.
type execJournalEvent struct {
	Kind   string
	ID     uint32
	Exec   string
	At     time.Time
	Data   []byte
	Call   *provider.ToolCall
	Stage  []uint32
	Result *savedExecOutcome
}
type savedExecOutcome struct {
	Text    string
	IsError bool
	Card    []byte
	Images  []provider.Image
}
type execJob struct {
	Call      provider.ToolCall
	Stages    []uint32
	Result    *savedExecOutcome
	Applied   bool
	Cancelled bool
}

func (s *Session) journalPath() string {
	if s.history == nil || s.RequestID == "" {
		return ""
	}
	return filepath.Join(s.history.dir, "executions", unsafeID.ReplaceAllString(s.RequestID, "_")+".journal")
}
func (s *Session) appendExecEventLocked(e execJournalEvent) {
	path := s.journalPath()
	if path == "" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	raw, err := json.Marshal(e)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0700)
	}
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			_, err = f.Write(append(raw, '\n'))
			if err == nil && (e.Kind == "dispatch" || e.Kind == "job" || e.Kind == "stage") {
				err = f.Sync()
			}
			ce := f.Close()
			if err == nil {
				err = ce
			}
		}
	}
	if err != nil {
		s.execBook.journalFailed = true
		slog.Error("execution journal unavailable", "run_instance", s.runInstance, "request", s.RequestID)
	}
}
func (s *Session) loadExecJournal(b *execBook) {
	path := s.journalPath()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		b.journalFailed = true
		return
	}
	defer f.Close()
	d := json.NewDecoder(f)
	for {
		var e execJournalEvent
		if err := d.Decode(&e); err != nil {
			if err != io.EOF {
				b.journalFailed = true
			}
			break
		}
		if e.ID > s.nextID {
			s.nextID = e.ID
		}
		switch e.Kind {
		case "dispatch":
			r := b.record(e.ID)
			r.sent = true
			r.dispatched = e.At
			r.activity = e.At
			s.execs.Store(e.ID, e.Exec)
		case "written":
			b.record(e.ID).written = e.At
		case "result":
			m := &cursorpb.ExecClientMessage{}
			if proto.Unmarshal(e.Data, m) == nil {
				r := b.record(e.ID)
				r.archive = append(r.archive, m)
				r.queue = append(r.queue, m)
				r.activity = e.At
			}
		case "close":
			r := b.record(e.ID)
			if !r.closed {
				r.closed = true
				r.closedAt = e.At
			}
		case "throw":
			b.record(e.ID).failed = true
		case "job":
			if e.Call != nil {
				b.jobs[e.Call.ID] = &execJob{Call: *e.Call, Stages: append([]uint32{}, e.Stage...)}
			}
		case "stage":
			if e.Call != nil {
				if j := b.jobs[e.Call.ID]; j != nil {
					j.Stages = append([]uint32{}, e.Stage...)
				}
			}
		case "complete":
			if e.Call != nil {
				if j := b.jobs[e.Call.ID]; j != nil {
					j.Result = e.Result
				}
			}
		case "cancel":
			if e.Call != nil {
				if j := b.jobs[e.Call.ID]; j != nil {
					j.Cancelled = true
				}
			}
		case "applied":
			if e.Call != nil {
				if j := b.jobs[e.Call.ID]; j != nil {
					j.Applied = true
					for _, id := range j.Stages {
						r := b.record(id)
						r.applied = true
						r.queue = nil
						r.archive = nil
					}
				}
			}
		}
	}
}
func (s *Session) beginExecJob(call provider.ToolCall, first uint32) *execJob {
	if s.history != nil && s.run != nil {
		if err := s.history.SaveRunModel(s.RequestID, s.run.GetConversationId(), s.Model.ID); err != nil {
			slog.Error("execution run index unavailable", "request", s.RequestID)
			b := s.book()
			b.mu.Lock()
			b.journalFailed = true
			b.mu.Unlock()
		}
	}
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[call.ID]
	if j == nil {
		if path := s.pendingExecPath(); path != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				b.journalFailed = true
			} else if err = os.WriteFile(path, nil, 0600); err != nil {
				b.journalFailed = true
			}
		}
		j = &execJob{Call: call, Stages: []uint32{first}}
		b.jobs[call.ID] = j
		s.appendExecEventLocked(execJournalEvent{Kind: "job", Call: &call, Stage: j.Stages})
	} else {
		// Rebuild Pending from immutable requests and the ordered recorded results.
		// Feed is local; only a genuinely new stage may ever be dispatched.
		for _, id := range j.Stages {
			r := b.record(id)
			r.queue = append([]*cursorpb.ExecClientMessage{}, r.archive...)
			r.resumed = time.Now()
		}
	}
	return j
}
func (s *Session) jobStage(j *execJob, index int, next *cursorpb.ExecServerMessage) {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	if index < len(j.Stages) {
		next.Id = j.Stages[index]
	} else {
		s.nextID++
		next.Id = s.nextID
		j.Stages = append(j.Stages, next.Id)
		s.appendExecEventLocked(execJournalEvent{Kind: "stage", Call: &j.Call, Stage: j.Stages})
	}
	next.ExecId = j.Call.ID + "-" + strconv.FormatUint(uint64(next.Id), 10)
}
func (s *Session) execWasSent(id uint32) bool {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.record(id).sent
}
func (s *Session) finishExecJob(out toolOutcome) {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[out.call.ID]
	if j == nil {
		return
	}
	var card []byte
	if out.card != nil {
		card, _ = proto.Marshal(out.card)
	}
	j.Result = &savedExecOutcome{Text: out.text, IsError: out.isErr, Card: card, Images: out.images}
	s.appendExecEventLocked(execJournalEvent{Kind: "complete", Call: &j.Call, Result: j.Result})
}
func (s *Session) hasPendingExec() bool {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, j := range b.jobs {
		if !j.Applied {
			return true
		}
	}
	return false
}
func (s *Session) markExecApplied(messages []provider.Message) {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, j := range b.jobs {
		if j.Result == nil || j.Applied {
			continue
		}
		for _, m := range messages {
			if m.Role == "tool" && m.ToolCallID == j.Call.ID && m.Content == j.Result.Text {
				j.Applied = true
				for _, id := range j.Stages {
					r := b.record(id)
					r.applied = true
					r.queue = nil
					r.archive = nil
				}
				s.appendExecEventLocked(execJournalEvent{Kind: "applied", Call: &j.Call})
				break
			}
		}
	}
	allApplied := true
	for _, j := range b.jobs {
		if !j.Applied {
			allApplied = false
		}
	}
	if allApplied && !b.journalFailed {
		if path := s.pendingExecPath(); path != "" {
			_ = os.Remove(path)
		}
	}

}
func savedOutcome(j *execJob) toolOutcome {
	var card *cursorpb.ToolCall
	if len(j.Result.Card) > 0 {
		card = &cursorpb.ToolCall{}
		_ = proto.Unmarshal(j.Result.Card, card)
	}
	return toolOutcome{call: j.Call, text: j.Result.Text, isErr: j.Result.IsError, images: j.Result.Images, card: card}
}
func (s *Session) resumeExecJobs(ctx context.Context, send Emit, messages []provider.Message) ([]provider.Message, string, error) {
	b := s.book()
	b.mu.Lock()
	var jobs []*execJob
	for _, j := range b.jobs {
		if !j.Applied {
			jobs = append(jobs, j)
		}
	}
	b.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Stages[0] < jobs[j].Stages[0] })
	for _, j := range jobs {
		var out toolOutcome
		var err error
		if j.Result != nil {
			out = savedOutcome(j)
		} else {
			exec, ui, pending, e := tools.Request(j.Stages[0], j.Call)
			if e != nil {
				return messages, "An unresolved editor execution could not be reconstructed. Inspect it before retrying.", nil
			}
			out, err = s.execLoop(ctx, send, j.Call, exec, ui, pending)
			if err != nil {
				return messages, "", err
			}
		}
		replaced := false
		hasCall := false
		for i := range messages {
			if messages[i].Role == "tool" && messages[i].ToolCallID == j.Call.ID {
				messages[i] = toolResult(out)
				replaced = true
			}
			for _, c := range messages[i].ToolCalls {
				if c.ID == j.Call.ID {
					hasCall = true
				}
			}
		}
		if !replaced {
			if !hasCall {
				messages = append(messages, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{j.Call}})
			}
			messages = append(messages, toolResult(out))
		}
		if out.pause != "" {
			return messages, out.pause, nil
		}
	}
	return messages, "", nil
}

// Pending markers prevent a new transport/run from hiding an unresolved side effect.
// They contain no arguments or results; execution data remains in the private journal.
func (s *Session) pendingExecPath() string {
	if s.history == nil || s.RequestID == "" || s.ConversationID() == "" {
		return ""
	}
	return filepath.Join(s.history.dir, "executions", "pending", unsafeID.ReplaceAllString(s.ConversationID(), "_"), unsafeID.ReplaceAllString(s.RequestID, "_"))
}
func (h *History) pendingExecRequests(conversation string) ([]string, error) {
	if h == nil || conversation == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(h.dir, "executions", "pending", unsafeID.ReplaceAllString(conversation, "_")))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}
func (s *Session) foreignPendingExec() bool {
	ids, err := s.history.pendingExecRequests(s.ConversationID())
	if err != nil {
		return true
	}
	for _, id := range ids {
		if id != unsafeID.ReplaceAllString(s.RequestID, "_") {
			return true
		}
	}
	return false
}

// Only a RunRequest may recover a pending owner; never a heartbeat or tool frame.
func (h *Hub) pendingExecutionSessionLocked(run *cursorpb.AgentRunRequest) *Session {
	ids, err := h.history.pendingExecRequests(run.GetConversationId())
	if err != nil || len(ids) != 1 {
		return nil
	}
	id := ids[0]
	for _, slot := range h.slots {
		if slot.session != nil && slot.session.RequestID == id {
			if !slot.session.Running() && slot.session.hasPendingExec() && slot.session.Model.ID == modelID(run) {
				return slot.session
			}
			return nil
		}
	}
	if f := h.finished[id]; f != nil {
		if !f.session.Running() && f.session.hasPendingExec() && f.session.Model.ID == modelID(run) {
			return f.session
		}
		return nil
	}
	conv, model := h.history.LoadRunModel(id)
	if conv != run.GetConversationId() || model != modelID(run) || h.lookup == nil {
		return nil
	}
	m, ok := h.lookup(model)
	if !ok {
		return nil
	}
	sess := newSession(id, m, &cursorpb.AgentRunRequest{ConversationId: &conv}, h.history)
	if !sess.hasPendingExec() {
		return nil
	}
	sess.finished.Store(true)
	sess.baseState = h.history.LoadState(conv)
	for _, id := range m.Fallback {
		if fb, ok := h.lookup(id); ok && fb.ID != m.ID {
			sess.Fallbacks = append(sess.Fallbacks, fb)
		}
	}
	slog.Debug("pending exec owner restored", "run_instance", sess.runInstance, "request", id)
	return sess
}

// Restore detached owners at startup, before a late frame can arrive. This does
// not bind a transport, adopt a RunRequest, or start a model/exec consumer.
func (h *Hub) restorePendingExecOwners() {
	if h.history == nil || h.lookup == nil {
		return
	}
	dirs, err := os.ReadDir(filepath.Join(h.history.dir, "executions", "pending"))
	if err != nil {
		return
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		ids, err := h.history.pendingExecRequests(dir.Name())
		if err != nil {
			continue
		}
		for _, id := range ids {
			conv, model := h.history.LoadRunModel(id)
			if conv == "" || unsafeID.ReplaceAllString(conv, "_") != dir.Name() {
				continue
			}
			m, ok := h.lookup(model)
			if !ok {
				continue
			}
			sess := newSession(id, m, &cursorpb.AgentRunRequest{ConversationId: &conv}, h.history)
			if !sess.hasPendingExec() {
				continue
			}
			sess.baseState = h.history.LoadState(conv)
			sess.finished.Store(true)
			for _, fid := range m.Fallback {
				if fb, ok := h.lookup(fid); ok && fb.ID != m.ID {
					sess.Fallbacks = append(sess.Fallbacks, fb)
				}
			}
			h.finished[id] = &finished{session: sess, at: time.Now()}
			if len(ids) == 1 {
				h.byConv[conv] = h.finished[id]
			}
		}
	}
}

func (s *Session) hasUnresolvedExec() bool {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, j := range b.jobs {
		if !j.Applied && j.Result == nil {
			return true
		}
	}
	return false
}

func (s *Session) cancelExecJob(callID string) {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	if j := b.jobs[callID]; j != nil {
		j.Cancelled = true
		s.appendExecEventLocked(execJournalEvent{Kind: "cancel", Call: &j.Call})
	}
}

func (s *Session) cancelledBeforeStage(j *execJob, index int) bool {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	return j.Cancelled && (index >= len(j.Stages) || !b.record(j.Stages[index]).sent)
}
