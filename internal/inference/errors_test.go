package inference

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/provider"
)

type endInfo struct {
	code      uint64
	hasDetail bool
	errEnum   uint64
	title     string
	detail    string
	retrySet  bool
	retryable bool
}

func decodeEnd(t *testing.T, errOut []field) endInfo {
	t.Helper()
	var e endInfo
	for _, f := range errOut {
		switch f.num {
		case 1:
			e.code = f.v
		case 3:
			d, _ := fields(f.b)
			if str(d, 1) != "aiserver.v1.ErrorDetails" {
				t.Fatalf("detail type %q", str(d, 1))
			}
			e.hasDetail = true
			ed, _ := child(d, 2)
			for _, x := range ed {
				if x.num == 1 {
					e.errEnum = x.v
				}
			}
			c, _ := child(ed, 2)
			e.title, e.detail = str(c, 1), str(c, 2)
			for _, x := range c {
				if x.num == 4 {
					e.retrySet, e.retryable = true, x.v != 0
				}
			}
		}
	}
	return e
}

// 跑一次 invocation，返回 invocation_end 的错误部分。
func endOf(t *testing.T, runs *Runs, run *Run, sink *frameSink, invID string, seq uint64) endInfo {
	t.Helper()
	inv, _ := DecodeClient(sampleInvoke(t, invID))
	run.Append(inv, seq, true)
	ms := sink.waitFor(t, hasEnd(invID))
	for _, m := range ms {
		if m.kind == 4 && m.inv == invID {
			return decodeEnd(t, m.errOut)
		}
	}
	t.Fatal("no end")
	return endInfo{}
}

func serveWith(t *testing.T, chat ChatFunc) (*Runs, *Run, *frameSink) {
	runs := &Runs{Heartbeat: time.Hour, Chat: chat}
	run := runs.Start("req-e", RunRequest{ModelID: "mine", ConversationID: "conv-e"}, config.Model{ID: "mine", DisplayName: "Broken"}, 0, true)
	sink := &frameSink{ch: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go runs.Serve(ctx, sink, nil, run)
	return runs, run, sink
}

func TestProviderErrorsStopWithMessage(t *testing.T) {
	cases := []struct {
		err   error
		title string
	}{
		{&provider.APIError{Kind: provider.KindAuth, Status: 401, Text: "401"}, "密钥被拒绝"},
		{&provider.APIError{Kind: provider.KindBadRequest, Status: 400, Text: "400"}, "拒绝了这次请求"},
		{&provider.APIError{Kind: provider.KindContextOverflow, Status: 400, Text: "too long"}, "对话太长"},
		{&provider.APIError{Kind: provider.KindRateLimit, Status: 429, Detail: "You exceeded your current quota"}, "额度用完"},
		{&provider.APIError{Kind: provider.KindBadRequest, Status: 402, Text: "402"}, "额度用完"},
	}
	for i, c := range cases {
		err := c.err
		runs, run, sink := serveWith(t, func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
			return provider.Message{}, err
		})
		_ = runs
		e := endOf(t, runs, run, sink, "inv", 1)
		if e.code != codeFailedPrecond || !e.hasDetail || e.errEnum != errCustomMessage || !e.retrySet || e.retryable {
			t.Fatalf("case %d: must be terminal CUSTOM_MESSAGE with is_retryable=false: %+v", i, e)
		}
		if strings.Contains(c.title, "额度") && strings.Contains(e.detail, "限流") {
			t.Fatalf("case %d: quota detail must not say rate limit: %q", i, e.detail)
		}
		if !strings.Contains(e.title, c.title) || !strings.Contains(e.title, "Broken") || e.detail == "" {
			t.Fatalf("case %d: title=%q detail=%q", i, e.title, e.detail)
		}
	}
}

func TestTransientErrorsRetryThenStop(t *testing.T) {
	var mu sync.Mutex
	fail := true
	runs, run, sink := serveWith(t, func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return provider.Message{}, &provider.APIError{Kind: provider.KindServer, Status: 503, Text: "503"}
		}
		return provider.Message{Role: "assistant", Content: "ok"}, nil
	})
	for i := 1; i <= maxTransientRounds; i++ {
		e := endOf(t, runs, run, sink, "t"+string(rune('0'+i)), uint64(i))
		if e.code != codeUnavailable || e.hasDetail {
			t.Fatalf("attempt %d must be retryable (no ErrorDetails): %+v", i, e)
		}
	}
	e := endOf(t, runs, run, sink, "t9", 3)
	if e.code != codeFailedPrecond || !e.hasDetail || e.retryable || !strings.Contains(e.title, "连不上") {
		t.Fatalf("after %d transient failures must stop: %+v", maxTransientRounds, e)
	}
	// 成功一次清零
	mu.Lock()
	fail = false
	mu.Unlock()
	if e := endOf(t, runs, run, sink, "ok", 4); e.code != 0 {
		t.Fatalf("success: %+v", e)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if e := endOf(t, runs, run, sink, "t10", 5); e.code != codeUnavailable {
		t.Fatalf("counter must reset after success: %+v", e)
	}
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestCancelStopsUpstreamImmediatelyAndLogs(t *testing.T) {
	logs := &logBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	started := make(chan struct{})
	upstreamGone := make(chan time.Time, 1)
	runs, run, sink := serveWith(t, func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
		onText("partial")
		close(started)
		select {
		case <-ctx.Done():
			upstreamGone <- time.Now()
			return provider.Message{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return provider.Message{}, errors.New("not cancelled")
		}
	})
	_ = runs
	inv, _ := DecodeClient(sampleInvoke(t, "inv-x"))
	run.Append(inv, 1, true)
	<-started
	at := time.Now()
	run.Append(ClientMessage{Kind: ClientCancelInvocation, CancelID: "inv-x"}, 2, true)
	select {
	case gone := <-upstreamGone:
		if d := gone.Sub(at); d > 200*time.Millisecond {
			t.Fatalf("upstream cancelled after %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream model call was not cancelled")
	}
	sink.waitFor(t, hasEnd("inv-x"))
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(logs.String(), "模型调用已取消") {
		if time.Now().After(deadline) {
			t.Fatalf("missing cancel log; got %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
