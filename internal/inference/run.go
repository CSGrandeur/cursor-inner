package inference

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

// ChatFunc 调一次模型（生产用 provider.Chat 绑好拨号器）。
type ChatFunc func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error)

// Connect 错误码
const (
	codeCanceled      = 1
	codeUnknown       = 2
	codeUnimplemented = 12
)

// Runs 记着本机接下的 RunInference 会话（按 BidiAppend 的 request id）。
type Runs struct {
	Chat      ChatFunc
	Heartbeat time.Duration // 默认 5s；客户端 30s 收不到帧就判定卡死

	mu        sync.Mutex
	runs      map[string]*Run
	transient map[string]int // 对话 → 连续临时失败次数
	wake      chan struct{}
}

type Run struct {
	ID    string
	Model config.Model
	Req   RunRequest

	created time.Time
	mu      sync.Mutex
	queue   []ClientMessage
	seqSet  bool
	next    uint64
	pending map[uint64]pendingMsg
	notify  chan struct{}
}

type pendingMsg struct {
	msg ClientMessage
	at  time.Time
}

// Start 登记一个新会话（run_request 已读出、模型已确认是本机的）。
func (r *Runs) Start(id string, req RunRequest, m config.Model, seq uint64, hasSeq bool) *Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]*Run{}
	}
	now := time.Now()
	for k, old := range r.runs {
		if now.Sub(old.created) > 6*time.Hour {
			delete(r.runs, k)
		}
	}
	run := &Run{ID: id, Model: m, Req: req, created: now, notify: make(chan struct{}, 1), pending: map[uint64]pendingMsg{}}
	if hasSeq {
		run.seqSet, run.next = true, seq+1
	}
	r.runs[id] = run
	if r.wake != nil {
		close(r.wake)
		r.wake = nil
	}
	return run
}

func (r *Runs) Get(id string) *Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

func (r *Runs) Has(id string) bool { return r.Get(id) != nil }

func (r *Runs) done(id string) {
	r.mu.Lock()
	delete(r.runs, id)
	r.mu.Unlock()
}

// Wait 等会话登记（RunInferenceSSE 可能先于 BidiAppend 到）。
func (r *Runs) Wait(ctx context.Context, id string, d time.Duration) *Run {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		r.mu.Lock()
		if run := r.runs[id]; run != nil {
			r.mu.Unlock()
			return run
		}
		if r.wake == nil {
			r.wake = make(chan struct{})
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-wake:
		case <-t.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// Append 收下一条客户端消息。按 append_seqno 排序；缺号超过 1 秒就不等了。
func (run *Run) Append(msg ClientMessage, seq uint64, hasSeq bool) {
	run.mu.Lock()
	switch {
	case !hasSeq || !run.seqSet:
		run.queue = append(run.queue, msg)
		if hasSeq {
			run.seqSet, run.next = true, seq+1
		}
	case seq < run.next:
		// 重发的旧帧
	case seq == run.next:
		run.queue = append(run.queue, msg)
		run.next++
		run.drainLocked(false)
	default:
		run.pending[seq] = pendingMsg{msg: msg, at: time.Now()}
	}
	run.mu.Unlock()
	run.poke()
}

func (run *Run) drainLocked(force bool) {
	for {
		if p, ok := run.pending[run.next]; ok {
			delete(run.pending, run.next)
			run.queue = append(run.queue, p.msg)
			run.next++
			continue
		}
		if !force || len(run.pending) == 0 {
			return
		}
		var oldest time.Time
		keys := make([]uint64, 0, len(run.pending))
		for k, p := range run.pending {
			keys = append(keys, k)
			if oldest.IsZero() || p.at.Before(oldest) {
				oldest = p.at
			}
		}
		if time.Since(oldest) < time.Second {
			return
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		run.next = keys[0]
	}
}

func (run *Run) flushStale() {
	run.mu.Lock()
	n := len(run.queue)
	run.drainLocked(true)
	grew := len(run.queue) > n
	run.mu.Unlock()
	if grew {
		run.poke()
	}
}

func (run *Run) poke() {
	select {
	case run.notify <- struct{}{}:
	default:
	}
}

func (run *Run) take() []ClientMessage {
	run.mu.Lock()
	defer run.mu.Unlock()
	q := run.queue
	run.queue = nil
	return q
}

// Serve 在 RunInferenceSSE 上回这条会话，直到 finish_run、客户端断开或 ctx 结束。
func (r *Runs) Serve(ctx context.Context, w io.Writer, flush func(), run *Run) error {
	defer r.done(run.ID)
	var wmu sync.Mutex
	write := func(b []byte, raw bool) error {
		wmu.Lock()
		defer wmu.Unlock()
		if !raw {
			b = protox.Frame(0, b)
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	}
	send := func(b []byte) error { return write(b, false) }
	display := run.Model.DisplayName
	if display == "" {
		display = run.Model.ID
	}
	if err := send(RunReady(ReadyInfo{ModelID: run.Req.ModelID, MaxMode: run.Req.MaxMode, DisplayName: display, ContextTokens: run.Model.ContextWindow})); err != nil {
		return err
	}
	beat := r.Heartbeat
	if beat <= 0 {
		beat = 5 * time.Second
	}
	ticker := time.NewTicker(beat)
	defer ticker.Stop()
	var wg sync.WaitGroup
	var imu sync.Mutex
	invs := map[string]context.CancelFunc{}
	cancelAll := func() {
		imu.Lock()
		for _, c := range invs {
			c()
		}
		imu.Unlock()
	}
	defer func() { cancelAll(); wg.Wait() }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := send(Heartbeat()); err != nil {
				return err
			}
			run.flushStale()
		case <-run.notify:
			for _, m := range run.take() {
				switch m.Kind {
				case ClientInvokeModel:
					ictx, cancel := context.WithCancel(ctx)
					imu.Lock()
					invs[m.Invoke.ID] = cancel
					imu.Unlock()
					wg.Add(1)
					go func(inv *Invoke) {
						defer wg.Done()
						defer func() {
							imu.Lock()
							delete(invs, inv.ID)
							imu.Unlock()
							cancel()
						}()
						r.invoke(ictx, run, inv, send)
					}(m.Invoke)
				case ClientCancelInvocation:
					imu.Lock()
					if c := invs[m.CancelID]; c != nil {
						c()
					}
					imu.Unlock()
				case ClientFinishRun:
					cancelAll()
					wg.Wait()
					return write(protox.EndStream(), true)
				}
			}
		}
	}
}

func (r *Runs) invoke(ctx context.Context, run *Run, inv *Invoke, send func([]byte) error) {
	id := inv.ID
	if inv.Unsupported != "" {
		slog.Warn("本地循环请求用了暂不支持的形式", "request", run.ID, "form", inv.Unsupported)
		_ = send(End(id, codeUnimplemented, fmt.Sprintf("cursor-inner does not support %s yet", inv.Unsupported)))
		return
	}
	_ = send(Response(id, InvocationIDInfo(id)))
	issuer := provider.ReasoningIssuer(run.Model)
	started := time.Now()
	reply, err := r.Chat(ctx, run.Model, inv.System, inv.Messages, inv.Tools,
		func(s string) error { return send(Response(id, TextDelta(s))) },
		func(s string) error { return send(Response(id, Thinking(s, "", issuer))) })
	if err != nil {
		if ctx.Err() != nil {
			slog.Info("本地循环：模型调用已取消，上游连接已断开", "request", run.ID, "invocation", id, "model", run.Model.DisplayName, "ms", time.Since(started).Milliseconds())
			_ = send(End(id, codeCanceled, "canceled"))
			return
		}
		key := r.convKey(run)
		f := classify(err, modelName(run), r.bumpTransient(key, true))
		if f.retryable {
			slog.Warn("本地循环：模型调用临时失败，交给 Cursor 重试", "request", run.ID, "model", run.Model.DisplayName, "error", f.message)
		} else {
			r.bumpTransient(key, false)
			slog.Warn("本地循环：模型调用失败，已停止这一回合", "request", run.ID, "model", run.Model.DisplayName, "error", f.message)
		}
		_ = send(endFor(id, f))
		return
	}
	r.bumpTransient(r.convKey(run), false)
	if reply.ReasoningSignature != "" {
		_ = send(Response(id, Thinking("", reply.ReasoningSignature, issuer)))
	}
	for i := range reply.ToolCalls {
		tc := &reply.ToolCalls[i]
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%s_%d", id, i)
		}
		if tc.Arguments == "" {
			tc.Arguments = "{}"
		}
		_ = send(Response(id, ToolCall(tc.ID, tc.Name, tc.Arguments, i)))
	}
	_ = send(Response(id, Usage(reply.PromptTokens, reply.CompletionTokens)))
	_ = send(Response(id, ExtendedUsage(reply.PromptTokens, reply.CompletionTokens, reply.CacheTokens)))
	_ = send(Response(id, ResponseInfo(id, run.Req.ModelID, started.UnixMilli(), reply, issuer)))
	_ = send(Response(id, TextFinal()))
	_ = send(End(id, 0, ""))
	slog.Info("本地循环一步已由本地模型回答", "request", run.ID, "model", run.Model.DisplayName, "tools", len(reply.ToolCalls), "ms", time.Since(started).Milliseconds())
}

func modelName(run *Run) string {
	if run.Model.DisplayName != "" {
		return run.Model.DisplayName
	}
	return run.Model.ID
}

func (r *Runs) convKey(run *Run) string {
	if run.Req.ConversationID != "" {
		return run.Req.ConversationID
	}
	return "model:" + run.Model.ID
}

// bumpTransient 返回此前的连续临时失败次数；fail=true 时加一，否则清零。
func (r *Runs) bumpTransient(key string, fail bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.transient == nil {
		r.transient = map[string]int{}
	}
	prev := r.transient[key]
	if fail {
		r.transient[key] = prev + 1
	} else {
		delete(r.transient, key)
	}
	return prev
}
