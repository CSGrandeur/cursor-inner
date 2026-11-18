package agent

import (
	"context"
	"cursor-inner/internal/cursorpb"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// An exec heartbeat renews an inactivity lease, never the absolute execution bound.
var execTotalLimit = 30 * time.Minute

// streamClose closes the client producer, not independent result transports.
// Drain in-flight results for a bounded interval, capped by the execution/context
// deadlines. Neither duplicate closes, heartbeats nor resume can renew it.
const execCloseDrainLimit = time.Second

var runInstances atomic.Uint64

type execWaitError struct {
	id     uint32
	reason string
}

func (e *execWaitError) Error() string { return fmt.Sprintf("editor exec %d: %s", e.id, e.reason) }

type execRecord struct {
	dispatched, written, activity, resumed, closedAt time.Time
	started, closed, failed, sent, applied           bool
	heartbeats                                       uint64
	queue, archive                                   []*cursorpb.ExecClientMessage
}
type execBook struct {
	mu            sync.Mutex
	records       map[uint32]*execRecord
	changed       chan struct{}
	jobs          map[string]*execJob
	journalFailed bool
}

func (s *Session) book() *execBook {
	s.bookOnce.Do(func() {
		s.execBook = &execBook{records: map[uint32]*execRecord{}, changed: make(chan struct{}, 1), jobs: map[string]*execJob{}}
		s.runInstance = runInstances.Add(1)
		s.loadExecJournal(s.execBook)
	})
	return s.execBook
}
func (b *execBook) signal() {
	select {
	case b.changed <- struct{}{}:
	default:
	}
}
func (b *execBook) record(id uint32) *execRecord {
	r := b.records[id]
	if r == nil {
		at := time.Now()
		r = &execRecord{dispatched: at, activity: at}
		b.records[id] = r
	}
	return r
}
func (s *Session) execDispatched(m *cursorpb.ExecServerMessage) {
	s.execs.Store(m.GetId(), m.GetExecId())
	b := s.book()
	b.mu.Lock()
	r := b.record(m.GetId())
	if !r.sent {
		r.sent = true
		s.appendExecEventLocked(execJournalEvent{Kind: "dispatch", ID: m.GetId(), Exec: m.GetExecId()})
	}
	b.mu.Unlock()
	slog.Debug("exec lifecycle", "run_instance", s.runInstance, "request", s.RequestID, "id", m.GetId(), "exec", m.GetExecId(), "phase", "dispatched")
}
func (s *Session) execWritten(id uint32, err error) {
	b := s.book()
	b.mu.Lock()
	r := b.record(id)
	if err == nil {
		r.written = time.Now()
		r.activity = r.written
		s.appendExecEventLocked(execJournalEvent{Kind: "written", ID: id})
	}
	b.mu.Unlock()
	phase := "written"
	if err != nil {
		phase = "write_failed"
	}
	slog.Debug("exec lifecycle", "run_instance", s.runInstance, "request", s.RequestID, "id", id, "phase", phase)
}
func controlID(m *cursorpb.ExecClientControlMessage) uint32 {
	switch x := m.GetMessage().(type) {
	case *cursorpb.ExecClientControlMessage_Heartbeat:
		return x.Heartbeat.GetId()
	case *cursorpb.ExecClientControlMessage_StreamClose:
		return x.StreamClose.GetId()
	case *cursorpb.ExecClientControlMessage_Throw:
		return x.Throw.GetId()
	}
	return 0
}
func (s *Session) ownsControl(m *cursorpb.ExecClientControlMessage) bool {
	_, ok := s.execs.Load(controlID(m))
	return ok
}
func (s *Session) deliverControl(m *cursorpb.ExecClientControlMessage) {
	id := controlID(m)
	if !s.ownsControl(m) {
		slog.Debug("exec control dropped", "request", s.RequestID, "id", id, "reason", "unknown_owner")
		return
	}
	b := s.book()
	b.mu.Lock()
	r := b.record(id)
	if r.applied {
		b.mu.Unlock()
		return
	}
	r.activity = time.Now()
	firstClose := false
	phase := "heartbeat"
	r.heartbeats++
	logHeartbeat := r.heartbeats == 1 || r.heartbeats%20 == 0
	switch m.GetMessage().(type) {
	case *cursorpb.ExecClientControlMessage_StreamClose:
		if !r.closed {
			r.closed = true
			r.closedAt = time.Now()
			firstClose = true
		}
		phase = "stream_close"
	case *cursorpb.ExecClientControlMessage_Throw:
		r.failed = true
		phase = "client_throw"
	}
	if firstClose {
		s.appendExecEventLocked(execJournalEvent{Kind: "close", ID: id, At: r.closedAt})
	}
	if phase == "client_throw" {
		s.appendExecEventLocked(execJournalEvent{Kind: "throw", ID: id})
	}
	b.mu.Unlock()
	b.signal()
	if phase != "heartbeat" || logHeartbeat {
		slog.Debug("exec control", "run_instance", s.runInstance, "request", s.RequestID, "id", id, "phase", phase)
	}
}

// Client error and stack strings are intentionally neither logged nor forwarded.
func (s *Session) enqueueExec(m *cursorpb.ExecClientMessage) {
	if m == nil {
		return
	}
	m = proto.Clone(m).(*cursorpb.ExecClientMessage)
	b := s.book()
	b.mu.Lock()
	r := b.record(m.GetId())
	if r.applied {
		b.mu.Unlock()
		slog.Debug("exec result dropped", "run_instance", s.runInstance, "request", s.RequestID, "id", m.GetId(), "reason", "checkpoint_committed")
		return
	}
	r.activity = time.Now()
	if m.GetShellStream().GetStart() != nil {
		r.started = true
	}
	r.queue = append(r.queue, m)
	r.archive = append(r.archive, m)
	data, _ := proto.Marshal(m)
	s.appendExecEventLocked(execJournalEvent{Kind: "result", ID: m.GetId(), Data: data})
	b.mu.Unlock()
	b.signal()
	slog.Debug("exec received", "run_instance", s.runInstance, "request", s.RequestID, "id", m.GetId(), "exec", m.GetExecId(), "phase", "queued", "kind", fmt.Sprintf("%T", m.GetMessage()))
}
func (s *Session) waitExec(ctx context.Context, ids map[uint32]int, idle, total time.Duration, steering bool) (*cursorpb.ExecClientMessage, error) {
	b := s.book()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.journalFailed {
			b.mu.Unlock()
			return nil, &execWaitError{reason: "journal_unavailable"}
		}
		at := time.Now()
		var firstErr error
		wait := time.Hour
		for id := range ids {
			r := b.record(id)
			if len(r.queue) > 0 {
				m := r.queue[0]
				r.queue = r.queue[1:]
				b.mu.Unlock()
				slog.Debug("exec consumed", "run_instance", s.runInstance, "request", s.RequestID, "id", id)
				return m, nil
			}
			reason := ""
			if r.failed {
				reason = "client_throw"
			} else if r.closed {
				left := r.closedAt.Add(execCloseDrainLimit).Sub(at)
				if left <= 0 {
					reason = "stream_closed_without_terminal_result"
				}
				if left < wait {
					wait = left
				}
			}
			if reason != "" && firstErr == nil {
				firstErr = &execWaitError{id, reason}
			}
			if idle > 0 && !r.closed {
				base := r.activity
				if r.resumed.After(base) {
					base = r.resumed
				}
				left := base.Add(idle).Sub(at)
				if left <= 0 && firstErr == nil {
					firstErr = &execWaitError{id, "inactive"}
				}
				if left < wait {
					wait = left
				}
			}
			if total > 0 {
				left := r.dispatched.Add(total).Sub(at)
				if left <= 0 && firstErr == nil {
					firstErr = &execWaitError{id, "execution_total_limit"}
				}
				if left < wait {
					wait = left
				}
			}
		}
		b.mu.Unlock()
		if firstErr != nil {
			return nil, firstErr
		}
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		var steer <-chan struct{}
		if steering {
			steer = s.steer
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-b.changed:
			timer.Stop()
		case <-steer:
			timer.Stop()
			if s.pullQueued() {
				return nil, errBreak
			}
		case <-timer.C:
		}
	}
}
func lifecycleError(err error) bool { var e *execWaitError; return errors.As(err, &e) }

func (s *Session) execJournalError() error {
	b := s.book()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.journalFailed {
		return &execWaitError{reason: "journal_unavailable"}
	}
	return nil
}
