package mitm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

func riMsg(num int, inner []byte) []byte { return protox.AppendBytes(nil, num, inner) }

func riAppend(id string, seq uint64, client []byte, binaryForm bool) []byte {
	var body []byte
	if binaryForm {
		body = protox.AppendBytes(nil, 4, client)
	} else {
		body = protox.AppendString(nil, 1, hex.EncodeToString(client))
	}
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, id))
	body = protox.AppendVarint(body, 3, seq)
	return protox.Frame(0, body)
}

// 形状照实抓到的 run_request：conversation_id 是 UUID，requested_model 带自定义模型 id，后面跟着几个布尔开关。
func riRunRequest(model string) []byte {
	rr := protox.AppendString(nil, 1, "dfb79122-0c4e-4f43-9a51-0d6c1f0c4a11")
	rr = protox.AppendBytes(rr, 3, protox.AppendString(nil, 1, model))
	rr = protox.AppendVarint(rr, 10, 1)
	rr = protox.AppendVarint(rr, 17, 1)
	rr = protox.AppendVarint(rr, 18, 1)
	return riMsg(1, rr)
}

func riInvoke(id string) []byte {
	user := protox.AppendVarint(nil, 1, 1)
	user = protox.AppendString(user, 2, "hi")
	sys := protox.AppendVarint(nil, 1, 4)
	sys = protox.AppendString(sys, 2, "SYS")
	req := protox.AppendBytes(nil, 1, sys)
	req = protox.AppendBytes(req, 1, user)
	inv := protox.AppendString(nil, 1, id)
	inv = protox.AppendBytes(inv, 2, req)
	return riMsg(2, inv)
}

type sseReader struct {
	frames chan []byte
}

func readFrames(r io.Reader) *sseReader {
	out := &sseReader{frames: make(chan []byte, 64)}
	go func() {
		br := bufio.NewReader(r)
		defer close(out.frames)
		for {
			head := make([]byte, 5)
			if _, err := io.ReadFull(br, head); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(head[1:])
			payload := make([]byte, n)
			if _, err := io.ReadFull(br, payload); err != nil {
				return
			}
			out.frames <- append(head[:1], payload...)
		}
	}()
	return out
}

// 返回下一帧的 RunInferenceServerMessage 分支号（0 = 结束帧）和内容。
func (s *sseReader) next(t *testing.T) (int, []byte) {
	select {
	case f, ok := <-s.frames:
		if !ok {
			t.Fatal("stream closed")
		}
		if f[0]&0x02 != 0 {
			return 0, f[1:]
		}
		num, _, n := protowire.ConsumeTag(f[1:])
		v, _ := protowire.ConsumeBytes(f[1+n:])
		return int(num), v
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for frame")
	}
	return -1, nil
}

func TestLocalLoopCustomModelAnsweredNatively(t *testing.T) {
	for _, sseFirst := range []bool{true, false} {
		const model = "62c2ab93e935104a"
		var upstream atomic.Int32
		s := New(func() config.Proxy { return config.Proxy{} },
			func() []catalog.Entry { return []catalog.Entry{{ID: model}} },
			func(id string) (config.Model, bool) {
				return config.Model{ID: model, DisplayName: "official-ds-v4.1-flash"}, id == model
			}, nil)
		s.nativeLoop = true
		s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			upstream.Add(1)
			return nil, http.ErrHandlerTimeout
		})}
		var gotSystem, gotUser string
		s.runs.Chat = func(ctx context.Context, m config.Model, system string, messages []provider.Message, tools []provider.Tool, onText, onThinking func(string) error) (provider.Message, error) {
			gotSystem, gotUser = system, messages[0].Content
			onText("Hello from local")
			return provider.Message{Role: "assistant", Content: "Hello from local"}, nil
		}
		srv := httptest.NewServer(http.HandlerFunc(s.route))
		const id = "7bccbc17-2b6e-4a7c-9c11-2f5b0f0e0001"
		post := func(path string, body []byte) *http.Response {
			resp, err := srv.Client().Post(srv.URL+path, "application/proto", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			return resp
		}
		appendMsg := func(seq uint64, client []byte) {
			resp := post("/aiserver.v1.BidiService/BidiAppend", riAppend(id, seq, client, seq%2 == 1))
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("append seq=%d status=%d", seq, resp.StatusCode)
			}
		}
		var stream *sseReader
		var sseResp *http.Response
		openSSE := func() {
			sseResp = post(runInferenceSSEPath, protox.Frame(0, protox.AppendString(nil, 1, id)))
			stream = readFrames(sseResp.Body)
		}
		if sseFirst {
			go func() { // SSE 先到，会等它
				time.Sleep(100 * time.Millisecond)
				if resp, err := srv.Client().Post(srv.URL+"/aiserver.v1.BidiService/BidiAppend", "application/proto", bytes.NewReader(riAppend(id, 0, riRunRequest(model), false))); err == nil {
					resp.Body.Close()
				}
			}()
			openSSE()
		} else {
			appendMsg(0, riRunRequest(model))
			openSSE()
		}
		if kind, _ := stream.next(t); kind != 2 {
			t.Fatalf("sseFirst=%v: want run_ready first, got %d", sseFirst, kind)
		}
		appendMsg(1, riInvoke("inv-1"))
		var text string
		for {
			kind, v := stream.next(t)
			if kind == 1 {
				continue
			}
			if kind == 4 {
				if protox.Child(v, 2) != nil {
					t.Fatalf("invocation error %x", v)
				}
				break
			}
			if kind == 3 {
				resp := protox.Child(v, 2)
				if tp := protox.Child(resp, 1); tp != nil {
					text += protox.StringField(tp, 1)
				}
			}
		}
		if text != "Hello from local" || gotSystem != "SYS" || gotUser != "hi" {
			t.Fatalf("text=%q system=%q user=%q", text, gotSystem, gotUser)
		}
		appendMsg(2, riMsg(4, nil))
		if kind, _ := stream.next(t); kind != 0 {
			t.Fatalf("want end-stream after finish_run, got %d", kind)
		}
		sseResp.Body.Close()
		srv.Close()
		if n := upstream.Load(); n != 0 {
			t.Fatalf("local-loop turn of a custom model went upstream %d times", n)
		}
	}
}

// CURSOR_INNER_LOCAL_LOOP=off：退回“给出说明并结束”。
func TestLocalLoopNativeOffFallsBackToNotice(t *testing.T) {
	t.Setenv("CURSOR_INNER_LOCAL_LOOP", "off")
	const model = "62c2ab93e935104a"
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: model}} },
		func(id string) (config.Model, bool) { return config.Model{ID: model}, id == model }, nil)
	if s.nativeLoop {
		t.Fatal("env off must disable native local loop")
	}
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", bytes.NewReader(riAppend("rid", 0, riRunRequest(model), false))))
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+runInferenceSSEPath, bytes.NewReader(protox.Frame(0, protox.AppendString(nil, 1, "rid")))))
	if b := rec.Body.Bytes(); len(b) < 5 || b[0] != 0x02 || !bytes.Contains(b, []byte("failed_precondition")) {
		t.Fatalf("want notice end frame, got %q", b)
	}
}
