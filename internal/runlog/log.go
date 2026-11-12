package runlog

import (
	"context"
	"sync"
)

// Note 是自定义模型一轮里的一步。只记种类、耗时和字节数，不记正文。
type Note struct {
	Kind      string `json:"kind"`
	Request   string `json:"request,omitempty"`
	Model     string `json:"model,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Status    int    `json:"status,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Tools     int    `json:"tools,omitempty"`
	Messages  int    `json:"messages,omitempty"`
	GapMs     int64  `json:"gap_ms,omitempty"`
	WaitMs    int64  `json:"wait_ms,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms,omitempty"`
	Prompt    int    `json:"prompt_tokens,omitempty"`
	Output    int    `json:"completion_tokens,omitempty"`
	Error     string `json:"error,omitempty"`
	// 上游诊断字段。VendorRequestID 只在供应商返回时填写，不编造。
	Provider        string `json:"provider,omitempty"`
	Effort          string `json:"effort,omitempty"`
	Roles           string `json:"roles,omitempty"`
	Chars           int    `json:"content_runes,omitempty"`
	Code            string `json:"code,omitempty"`
	ErrorType       string `json:"error_type,omitempty"`
	VendorRequestID string `json:"vendor_request_id,omitempty"`
	// 与同一回合上一次模型请求的结构差分（只记计数，不记正文）。
	DeltaMessages int `json:"delta_messages,omitempty"`
	DeltaRunes    int `json:"delta_runes,omitempty"`
	DeltaBytes    int `json:"delta_bytes,omitempty"`
}

type sinkFunc func(Note)

var (
	mu   sync.Mutex
	sink sinkFunc
)

type requestKey struct{}

// WithRequest 让这一轮模型调用能对上同一次会话。
func WithRequest(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestKey{}, id)
}

func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestKey{}).(string)
	return id
}

// Set 挂上记录。传 nil 等于关掉。没挂上时 Emit 什么都不做。
func Set(fn func(Note)) {
	mu.Lock()
	sink = fn
	mu.Unlock()
}

// Enabled 表示已经挂上记录。
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return sink != nil
}

// Emit 记下这一步。记录函数自己的失败不影响这一轮。
func Emit(n Note) {
	mu.Lock()
	fn := sink
	mu.Unlock()
	if fn == nil {
		return
	}
	defer func() { _ = recover() }()
	fn(n)
}
