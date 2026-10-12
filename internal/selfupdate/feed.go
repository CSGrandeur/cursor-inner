package selfupdate

import (
	"context"
	"sync"
)

// 进度只有一份：Updater.st。控制台的进度条和配置页的进度条都订阅它（Watch），
// 百分比、剩余时间这些派生数字也在这里算好，两边显示的数字因此完全一致。

// watchQueueMax 限制一个订阅者积压的快照数。正常情况下同一阶段的进度会合并成一条，积压只来自阶段切换。
const watchQueueMax = 256

// Watch 是一个进度订阅。Next 依次返回状态快照：同一阶段（状态、路径、错误都没变）里的进度更新会合并，
// 只留最新一条；阶段切换（检查 → 下载 → 校验 → 交接、换路径、失败）一条都不丢，订阅者据此打印状态行。
type Watch struct {
	u      *Updater
	mu     sync.Mutex
	q      []Status
	ready  chan struct{}
	closed bool
}

// Watch 订阅状态变化。第一条快照是订阅时的当前状态。用完调用 Close。
func (u *Updater) Watch() *Watch {
	w := &Watch{u: u, ready: make(chan struct{}, 1)}
	u.mu.Lock()
	if u.watchers == nil {
		u.watchers = map[*Watch]struct{}{}
	}
	u.watchers[w] = struct{}{}
	w.push(u.st)
	u.mu.Unlock()
	return w
}

// Next 等下一条快照。ctx 结束或 Close 之后返回错误。
func (w *Watch) Next(ctx context.Context) (Status, error) {
	for {
		w.mu.Lock()
		if len(w.q) > 0 {
			st := w.q[0]
			w.q = w.q[1:]
			w.mu.Unlock()
			return st, nil
		}
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return Status{}, context.Canceled
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-w.ready:
		}
	}
}

// Close 取消订阅。可以重复调用。
func (w *Watch) Close() {
	w.u.mu.Lock()
	delete(w.u.watchers, w)
	w.u.mu.Unlock()
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	select {
	case w.ready <- struct{}{}:
	default:
	}
}

func (w *Watch) push(st Status) {
	w.mu.Lock()
	if n := len(w.q); n > 0 && SameStage(w.q[n-1], st) {
		w.q[n-1] = st
	} else {
		if n >= watchQueueMax {
			w.q = w.q[1:]
		}
		w.q = append(w.q, st)
	}
	w.mu.Unlock()
	select {
	case w.ready <- struct{}{}:
	default:
	}
}

// SameStage 判断两条快照是否属于同一阶段：只有进度数字不同。
func SameStage(a, b Status) bool {
	return a.State == b.State && a.DLRoute == b.DLRoute && a.DLProbe == b.DLProbe &&
		a.Error == b.Error && a.ErrorCode == b.ErrorCode && a.Latest == b.Latest && a.Asset == b.Asset
}

// publishLocked 在 u.mu 持有时调用：补齐派生字段、递增序号，发给所有订阅者。
func (u *Updater) publishLocked() {
	s := &u.st
	s.Seq++
	s.Percent, s.ETA = 0, 0
	if s.Total > 0 {
		done := min(max(s.Done, 0), s.Total)
		s.Percent = int(done * 100 / s.Total)
		if s.Speed > 0 && s.Total > done {
			s.ETA = (s.Total - done + s.Speed - 1) / s.Speed
		}
	}
	for w := range u.watchers {
		w.push(*s)
	}
}
