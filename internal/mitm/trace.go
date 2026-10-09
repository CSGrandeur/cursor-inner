package mitm

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// Trace 收到一次已经交给 Cursor 的往来。为 nil 时转发和改写保持原样。
// 回调里的切片和头只在调用期间有效。
type Trace func(TraceEvent)

// TraceEvent 是 *.cursor.sh 上的一次 HTTP，或一条没有解密的隧道。
// 不按路径挑选：认识的接口和不认识的接口都走这里。
type TraceEvent struct {
	Host       string
	Method     string
	Path       string
	RawQuery   string
	Status     int
	ReqHeader  http.Header
	RespHeader http.Header
	ReqBody    []byte
	RespBody   []byte
	Tunnel     bool
}

var (
	readyMu   sync.Mutex
	readyFn   func(dir string)
	traceMu   sync.Mutex
	traceFn   Trace
	traceNote string
)

// OnReady 在数据目录确定后调用一次。没人挂时什么都不做。
func OnReady(fn func(dir string)) {
	readyMu.Lock()
	readyFn = fn
	readyMu.Unlock()
}

// NotifyReady 通知已经挂上的准备函数。
func NotifyReady(dir string) {
	readyMu.Lock()
	fn := readyFn
	readyMu.Unlock()
	if fn != nil {
		fn(dir)
	}
}

// SetTrace 挂上记录。note 为空表示没有额外记录。
func SetTrace(fn Trace, note string) {
	traceMu.Lock()
	traceFn = fn
	traceNote = note
	traceMu.Unlock()
}

// TraceNote 返回 SetTrace 留下的说明。没有记录时为空。
func TraceNote() string {
	traceMu.Lock()
	defer traceMu.Unlock()
	return traceNote
}

func currentTrace() Trace {
	traceMu.Lock()
	defer traceMu.Unlock()
	return traceFn
}

func reportTrace(fn Trace, ev TraceEvent) {
	if fn == nil {
		return
	}
	defer func() { _ = recover() }()
	fn(ev)
}

type bodyTee struct {
	rc  io.ReadCloser
	buf bytes.Buffer
}

func (b *bodyTee) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.buf.Write(p[:n])
	}
	return n, err
}

func (b *bodyTee) Close() error { return b.rc.Close() }

type traceWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
	body   bytes.Buffer
}

func (w *traceWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *traceWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.body.Write(p[:n])
	}
	return n, err
}

func (w *traceWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *traceWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
