package mitm

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
)

// 现场（2026-10-21 13:31–13:51）：Cursor 3.26 开了 agent_host_local_loop，BidiAppend 里装的是
// InferenceClientMessage（RunInference 协议），不是 AgentClientMessage。旧代码把它“挽救”成本地回合吞掉，
// 回程 RunInferenceSSE 转给官方，官方什么也没收到，客户端 “Taking longer than expected...” 600 秒后重试。
// 现在：BidiAppend 收下不转发，RunInferenceSSE 在本机立即以一个不可重试的错误结束，带说明。
func localLoopAppend(id, model string) []byte {
	// 一个读不成 AgentClientMessage 的负载（未配对的 end-group），里面带着自定义模型 id。
	var inner []byte
	inner = protowire.AppendTag(inner, 9, protowire.BytesType)
	inner = protowire.AppendBytes(inner, protox.AppendString(nil, 1, model))
	inner = protowire.AppendTag(inner, 50, protowire.EndGroupType)
	client := protox.AppendBytes(nil, 1, inner)
	body := protox.AppendString(nil, 1, hex.EncodeToString(client))
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, id))
	return protox.Frame(0, body)
}

func TestLocalLoopCustomModelEndsWithNoticeInsteadOfHanging(t *testing.T) {
	const model = "62c2ab93e935104a"
	const id = "99f57c22-3aa0-4db2-82e0-1e5cb67aeae4"
	upstream := 0
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: model}} },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		upstream++
		return nil, http.ErrHandlerTimeout
	})}

	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", bytes.NewReader(localLoopAppend(id, model))))
	if rec.Code != http.StatusOK {
		t.Fatalf("BidiAppend code=%d body=%q", rec.Code, rec.Body.String())
	}

	start := time.Now()
	rec = httptest.NewRecorder()
	sse := protox.Frame(0, protox.AppendString(nil, 1, id))
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+runInferenceSSEPath, bytes.NewReader(sse)))
	if time.Since(start) > 2*time.Second {
		t.Fatal("RunInferenceSSE should end right away")
	}
	if upstream != 0 {
		t.Fatalf("custom-model local-loop request went upstream %d times", upstream)
	}
	raw := rec.Body.Bytes()
	if len(raw) < 5 || raw[0] != 0x02 {
		t.Fatalf("want a Connect end-stream frame, got %q", raw)
	}
	var end struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Type, Value string
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw[5:], &end); err != nil {
		t.Fatal(err)
	}
	if end.Error.Code != "failed_precondition" || !strings.Contains(end.Error.Message, "cursor-inner") {
		t.Fatalf("end=%+v", end)
	}
	if len(end.Error.Details) != 1 || end.Error.Details[0].Type != "aiserver.v1.ErrorDetails" {
		t.Fatalf("details=%+v", end.Error.Details)
	}
	if _, err := base64.RawStdEncoding.DecodeString(end.Error.Details[0].Value); err != nil {
		t.Fatal(err)
	}
}

// 没被标记的 RunInferenceSSE（官方模型的本地循环）照常转发，不能被拦。
func TestRunInferenceSSEOfOfficialModelIsForwarded(t *testing.T) {
	old := localLoopWait
	localLoopWait = 50 * time.Millisecond
	t.Cleanup(func() { localLoopWait = old })
	upstream := 0
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: "62c2ab93e935104a"}} },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		upstream++
		return nil, http.ErrHandlerTimeout
	})}
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+runInferenceSSEPath, bytes.NewReader(protox.Frame(0, protox.AppendString(nil, 1, "official-run")))))
	if upstream == 0 {
		t.Fatal("official local-loop stream must be forwarded")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
