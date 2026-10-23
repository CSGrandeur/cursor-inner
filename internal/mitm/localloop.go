package mitm

import (
	"context"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/inference"
	"cursor-inner/internal/provider"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/i18n"
	"cursor-inner/internal/protox"
)

// Cursor 3.26 起，开启 agent_host_local_loop 的客户端在本机跑 agent 循环，只向服务端要推理：
// 走 aiserver.v1.InferenceService/RunInference（BidiAppend 里装的是 InferenceClientMessage，
// 不是 AgentClientMessage；回程是 RunInferenceSSE）。cursor-inner 还不会回答这种协议。
// 交给官方只会得到“未知模型”或一直等不到回应，所以带自定义模型的这类请求在本机直接结束，
// 并告诉用户原因和怎么办。

const runInferenceSSEPath = "/aiserver.v1.InferenceService/RunInferenceSSE"

// localLoopWait 是 RunInferenceSSE 先到、BidiAppend 后到时最多等多久。
var localLoopWait = 5 * time.Second

type localLoopSet struct {
	mu   sync.Mutex
	ids  map[string]time.Time
	wake chan struct{}
}

func (l *localLoopSet) mark(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = map[string]time.Time{}
	}
	now := time.Now()
	for k, at := range l.ids {
		if now.Sub(at) > 30*time.Minute {
			delete(l.ids, k)
		}
	}
	l.ids[id] = now
	if l.wake != nil {
		close(l.wake)
		l.wake = nil
	}
}

func (l *localLoopSet) has(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.ids[id]
	return ok
}

func (l *localLoopSet) wait(ctx context.Context, id string, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		l.mu.Lock()
		if _, ok := l.ids[id]; ok {
			l.mu.Unlock()
			return true
		}
		if l.wake == nil {
			l.wake = make(chan struct{})
		}
		wake := l.wake
		l.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

var localLoopUnsupported = i18n.T(
	"这次对话 Cursor 用了新的“本地循环”方式发起，cursor-inner 暂时还不能用自定义模型回答这种请求。请先改用官方模型，或等 cursor-inner 更新后再试。",
	"Cursor started this turn in its new local-loop mode, which cursor-inner cannot answer with a custom model yet. Use an official model for now, or try again after updating cursor-inner.")

var localLoopTitle = i18n.T("自定义模型暂不支持这种对话方式", "Custom model can't run this turn")

func (s *Server) runInferenceSSE(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	plain, err := protox.Plain(body, r.Header.Get("Content-Encoding"))
	id := ""
	if err == nil {
		id, err = protox.DecodeRunID(plain)
	}
	if err == nil {
		s.sseSeen.mark(id)
	}
	if err != nil || !s.localLoop.wait(r.Context(), id, localLoopWait) {
		withBody(r, body)
		s.forward(w, r)
		return
	}
	if run := s.runs.Get(id); run != nil {
		slog.Info("RunInferenceSSE 由本地模型回答", "request", id, "model", run.Model.DisplayName)
		s.countLocal()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("connect-protocol-version", "1")
		w.WriteHeader(http.StatusOK)
		flush(w)
		if err := s.runs.Serve(r.Context(), w, func() { flush(w) }, run); err != nil && r.Context().Err() == nil {
			slog.Debug("本地循环会话结束", "request", id, "error", err)
		}
		return
	}
	slog.Warn("自定义模型的本地循环请求已在本机结束", "request", id)
	writeConnectError(w, "failed_precondition", localLoopTitle.String(), localLoopUnsupported.String())
}

// writeConnectError 写一个 Connect 服务端流的结束帧，带 aiserver.v1.ErrorDetails，
// 让 Cursor 显示标题和说明并停止等待（不重试）。
func writeConnectError(w http.ResponseWriter, code, title, detail string) {
	var custom []byte
	custom = protox.AppendString(custom, 1, title)
	custom = protox.AppendString(custom, 2, detail)
	custom = protox.AppendVarint(custom, 4, 0) // is_retryable = false
	details := protox.AppendBytes(nil, 2, custom)
	end := map[string]any{"error": map[string]any{
		"code":    code,
		"message": detail,
		"details": []map[string]string{{"type": "aiserver.v1.ErrorDetails", "value": base64.RawStdEncoding.EncodeToString(details)}},
	}}
	raw, _ := json.Marshal(end)
	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(protox.Frame(0x02, raw))
}

// nativeLoopEnabled：CURSOR_INNER_LOCAL_LOOP=off 时不原生回答，退回 54d0412 的“说明并结束”。
func nativeLoopEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("CURSOR_INNER_LOCAL_LOOP")), "off")
}

// appendSeq 读 BidiAppendRequest.append_seqno（字段 3）。
func appendSeq(plain []byte) (uint64, bool) {
	body, _ := protox.Unary(plain)
	fs, err := protox.Fields(body)
	if err != nil {
		return 0, false
	}
	for _, f := range fs {
		if f.Num == 3 && f.Wire == 0 {
			return f.Val, true
		}
	}
	return 0, false
}

// startLocalLoop 在读不成 AgentClientMessage 时试着按 RunInferenceClientMessage 读：
// 是 run_request 且模型是本机的，就登记一个原生会话。
func (s *Server) startLocalLoop(reqID string, client, plain []byte) bool {
	if !s.nativeLoop {
		return false
	}
	m, err := inference.DecodeClient(client)
	if err != nil || m.Kind != inference.ClientRunRequest {
		return false
	}
	model, ok := s.lookup(m.Run.ModelID)
	if !ok {
		return false
	}
	if s.runs.Chat == nil {
		s.runs.Chat = s.chatForLocalLoop
	}
	seq, hasSeq := appendSeq(plain)
	s.runs.Start(reqID, *m.Run, model, seq, hasSeq)
	s.localLoop.mark(reqID)
	slog.Info("Cursor 用本地循环发起了自定义模型的回合，由 cursor-inner 原生回答", "request", reqID, "model", model.DisplayName)
	return true
}

// localLoopAppend 把属于已登记会话的 BidiAppend 交给它（invoke_model、cancel、finish 等）。
func (s *Server) localLoopAppend(w http.ResponseWriter, plain []byte) bool {
	reqID, client, err := protox.DecodeBidi(plain)
	if err != nil {
		return false
	}
	run := s.runs.Get(reqID)
	if run == nil {
		// 已经为这个 id 打开过 RunInferenceSSE：不必等 AgentClientMessage 解析失败，直接按本地循环读。
		if s.sseSeen.has(reqID) && s.startLocalLoop(reqID, client, plain) {
			w.Header().Set("Content-Type", "application/proto")
			w.WriteHeader(http.StatusOK)
			return true
		}
		return false
	}
	m, err := inference.DecodeClient(client)
	if err != nil {
		slog.Warn("本地循环消息读不懂，已忽略", "request", reqID, "error", err)
	} else {
		seq, hasSeq := appendSeq(plain)
		run.Append(m, seq, hasSeq)
	}
	w.Header().Set("Content-Type", "application/proto")
	w.WriteHeader(http.StatusOK)
	return true
}

func (s *Server) chatForLocalLoop(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
	d, err := dialer.ForModel(s.proxy(), m.UseProxy)
	if err != nil {
		return provider.Message{}, err
	}
	return provider.Chat(ctx, m, d, system, messages, tools, onText, onThinking)
}
