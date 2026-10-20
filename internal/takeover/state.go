package takeover

import (
	"sync"
	"time"
)

// 接管状态是三态：配置页绝不能把“还没确定”显示成错误。
//
//	pending 配置了接管但还没定论：刚启动、热交接中（交出或接入）、接管刚掉线还在宽限期内；
//	active  接管已生效；
//	error   明确失败：有错误、接管在宽限/超时后仍没生效。
//
// off 表示没有要求接管（没配置，或这次启动明确跳过）。
const (
	StateOff     = "off"
	StatePending = "pending"
	StateActive  = "active"
	StateError   = "error"
)

// 状态的原因，给配置页挑文案、给日志和排查用。
const (
	ReasonStarting  = "starting"   // 刚启动，接管正在建立
	ReasonHandover  = "handover"   // 热交接中（旧进程交出 / 新进程接入）
	ReasonSettling  = "settling"   // 接管没生效，宽限期内再确认
	ReasonProblem   = "problem"    // 有明确的错误文本
	ReasonNotActive = "not_active" // 宽限期过后仍没生效
	ReasonTimeout   = "timeout"    // 启动 / 交接超过上限还没接上
	ReasonSkipped   = "skipped"    // 这次启动用 --no-takeover 明确跳过
)

// Phase 是进程所处的阶段，由启动流程和热交接设置。
type Phase int

const (
	PhaseStarting Phase = iota // 刚启动，接管还没建立（默认）
	PhaseHandover              // 热交接中
	PhaseSettled               // 启动 / 交接流程已走完，之后接管没生效就是真的没生效
)

// 默认的宽限和上限。
const (
	DefaultGrace      = 10 * time.Second // 流程走完后接管仍没生效：多等这么久再判定失败，吸收短暂抖动
	DefaultPendingMax = 45 * time.Second // 启动 / 交接最多挂这么久，超过算失败，不能永远转圈
)

// Status 是一次判定的结果。Since 是进入当前状态（含原因）的时刻，页面和日志用。
type Status struct {
	State  string
	Reason string
	Since  time.Time
}

// Tracker 记录阶段和“最早发现没生效”的时刻，把原始事实判成三态。线程安全；时钟可注入，便于测试。
type Tracker struct {
	mu            sync.Mutex
	now           func() time.Time
	grace, maxPen time.Duration
	phase         Phase
	phaseSince    time.Time
	inactiveSince time.Time
	skipped       bool
	last          Status
}

// NewTracker 新建一个处于 PhaseStarting 的跟踪器。now 为 nil 用系统时钟。
func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	t := &Tracker{now: now, grace: DefaultGrace, maxPen: DefaultPendingMax}
	t.phaseSince = now()
	return t
}

// SetLimits 调整宽限和上限（测试用）。
func (t *Tracker) SetLimits(grace, pendingMax time.Duration) {
	t.mu.Lock()
	t.grace, t.maxPen = grace, pendingMax
	t.mu.Unlock()
}

// SetPhase 切换阶段。回到 PhaseSettled 时重新计宽限，刚交接完 / 回滚后给接管一点时间恢复。
func (t *Tracker) SetPhase(p Phase) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase != p {
		t.phase = p
		t.phaseSince = t.now()
		t.inactiveSince = time.Time{}
	}
}

// SetSkipped 记录这次启动明确不接管（--no-takeover）。
func (t *Tracker) SetSkipped(v bool) {
	t.mu.Lock()
	t.skipped = v
	t.mu.Unlock()
}

// Judge 把原始事实判成三态。configured 是配置里开着接管，active 是接管当前是否生效，
// problem 是有没有明确的错误文本。
func (t *Tracker) Judge(configured, active, problem bool) Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	set := func(state, reason string) Status {
		if t.last.State != state || t.last.Reason != reason || t.last.Since.IsZero() {
			t.last = Status{State: state, Reason: reason, Since: now}
		}
		return t.last
	}
	if active {
		t.inactiveSince = time.Time{}
	}
	switch {
	case problem:
		return set(StateError, ReasonProblem)
	case active:
		return set(StateActive, "")
	case t.skipped:
		return set(StateOff, ReasonSkipped)
	case !configured:
		return set(StateOff, "")
	}
	switch t.phase {
	case PhaseStarting, PhaseHandover:
		reason := ReasonStarting
		if t.phase == PhaseHandover {
			reason = ReasonHandover
		}
		if now.Sub(t.phaseSince) >= t.maxPen {
			return set(StateError, ReasonTimeout)
		}
		return set(StatePending, reason)
	}
	// 流程已走完但接管没生效：从第一次发现起给一段宽限，之后才算失败。
	if t.inactiveSince.IsZero() {
		t.inactiveSince = now
		if t.phaseSince.After(t.inactiveSince) {
			t.inactiveSince = t.phaseSince
		}
	}
	from := t.inactiveSince
	if t.phaseSince.After(from) {
		from = t.phaseSince
	}
	if now.Sub(from) < t.grace {
		return set(StatePending, ReasonSettling)
	}
	return set(StateError, ReasonNotActive)
}
