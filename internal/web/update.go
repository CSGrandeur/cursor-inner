package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"cursor-inner/internal/selfupdate"
)

// UpdateBackend 是可选的自更新接口；后端没实现时配置页不显示更新区。
type UpdateBackend interface {
	UpdateStatus() (selfupdate.Status, error)
	CheckUpdate(auto bool) (selfupdate.Status, error)
	ApplyUpdate() error
}

// UpdateWatcher 是可选的推送接口：实现了它，配置页用 Server-Sent Events 实时收进度；
// 没实现时 /api/update/events 返回 404，页面退回轮询 /api/update。
type UpdateWatcher interface {
	WatchUpdate() (*selfupdate.Watch, error)
}

// sseHeartbeat：没有变化时隔这么久发一条注释，让中间环节和浏览器知道连接还活着。
const sseHeartbeat = 15 * time.Second

// streams 管理进行中的推送连接。服务器关闭或交接时先结束它们：
// 浏览器的 EventSource 会自己重连，连到的就是（新的）监听者。
type streams struct {
	mu      sync.Mutex
	closing chan struct{}
}

func newStreams() *streams { return &streams{closing: make(chan struct{})} }

func (s *streams) done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// closeAll 结束现有连接；之后新来的连接照常服务（交接失败、收回监听时不需要额外操作）。
func (s *streams) closeAll() {
	s.mu.Lock()
	close(s.closing)
	s.closing = make(chan struct{})
	s.mu.Unlock()
}

func registerUpdate(mux *http.ServeMux, up UpdateBackend, st *streams) {
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		st, err := up.UpdateStatus()
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /api/update/events", func(w http.ResponseWriter, r *http.Request) {
		wu, ok := up.(UpdateWatcher)
		if !ok {
			http.NotFound(w, r)
			return
		}
		watch, err := wu.WatchUpdate()
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		defer watch.Close()
		serveUpdateEvents(w, r, watch, st.done())
	})
	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Auto bool `json:"auto"`
		}
		_ = readJSON(r, &body)
		st, err := up.CheckUpdate(body.Auto)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if err := up.ApplyUpdate(); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		st, _ := up.UpdateStatus()
		writeJSON(w, http.StatusAccepted, st)
	})
}

// serveUpdateEvents 把每条状态快照作为一条 SSE 消息推给页面（id 是快照序号）。
// 进度快照在 selfupdate 里已经按阶段合并过，这里不再节流。
func serveUpdateEvents(w http.ResponseWriter, r *http.Request, watch *selfupdate.Watch, closing <-chan struct{}) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// 断开后 1 秒重连：交接时旧进程结束推送，页面很快连上新进程。
	_, _ = fmt.Fprint(w, "retry: 1000\n\n")
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-closing:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		wctx, wcancel := context.WithTimeout(ctx, sseHeartbeat)
		st, err := watch.Next(wctx)
		wcancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if _, werr := fmt.Fprint(w, ": ping\n\n"); werr != nil {
				return
			}
			flusher.Flush()
			continue
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", st.Seq, raw); err != nil {
			return
		}
		flusher.Flush()
	}
}

// handler 是配置页的 http.Handler，另外记着推送连接，好在关闭、交接时结束它们。
type handler struct {
	http.Handler
	push *streams
}

// CloseStreams 结束 h 上所有进行中的推送连接（/api/update/events）。
// 服务器 Shutdown 前、交接时交出监听后调用：Shutdown 会等活动连接，推送连接不结束就要等到超时；
// 交接时结束推送，浏览器重连就会连到新进程。h 不是 HandlerVersion 返回的就什么都不做。
func CloseStreams(h http.Handler) {
	if x, ok := h.(*handler); ok {
		x.push.closeAll()
	}
}
