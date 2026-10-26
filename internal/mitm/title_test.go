package mitm

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"

	"google.golang.org/protobuf/proto"
)

func TestDeriveChatTitle(t *testing.T) {
	msg := protox.AppendString(nil, 1, "  hello   world  \nsecond line")
	body := protox.AppendBytes(nil, 1, msg)
	if got := deriveChatTitle(body); got != "hello world" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("一二三四五六七八九十", 8) // 80 runes
	msg = protox.AppendString(nil, 1, long)
	body = protox.AppendBytes(nil, 1, msg)
	got := deriveChatTitle(body)
	if got == "" || got == long || len([]rune(got)) > 61 {
		t.Fatalf("expected shortened title, got %q (runes=%d)", got, len([]rune(got)))
	}
	if deriveChatTitle(nil) != "" {
		t.Fatal("empty body should yield empty title")
	}
}

func TestNameTabLocalCustomModel(t *testing.T) {
	const modelID = "DeepSeek-V4-Flash-Vision-Exp"
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry {
			return []catalog.Entry{{ID: modelID, DisplayName: "DeepSeek"}}
		},
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	var forwarded bool
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		forwarded = true
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/proto"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}

	msg := protox.AppendString(nil, 1, "请帮我改这个函数")
	body := protox.AppendBytes(nil, 1, msg)
	body = protox.AppendString(body, 2, "conv-1")
	body = protox.AppendString(body, 99, modelID) // 合法字段里夹带自定义模型 id

	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+nameTabPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/proto")
	rec := httptest.NewRecorder()
	s.route(rec, req)
	if forwarded {
		t.Fatal("custom-model NameTab must not forward")
	}
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/proto" {
		t.Fatalf("content-type %q", rec.Header().Get("Content-Type"))
	}
	title := protox.StringField(rec.Body.Bytes(), 1)
	if title != "请帮我改这个函数" {
		t.Fatalf("title=%q", title)
	}
}

func TestNameTabOfficialForwards(t *testing.T) {
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: "DeepSeek-V4-Flash-Vision-Exp"}} },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	var gotPath string
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/proto"}}, Body: io.NopCloser(bytes.NewReader(protox.AppendString(nil, 1, "Official Title")))}, nil
	})}
	msg := protox.AppendString(nil, 1, "hello official")
	body := protox.AppendBytes(nil, 1, msg)
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+nameTabPath, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.route(rec, req)
	if gotPath != nameTabPath {
		t.Fatalf("path=%q", gotPath)
	}
	if protox.StringField(rec.Body.Bytes(), 1) != "Official Title" {
		t.Fatalf("body=%q", rec.Body.Bytes())
	}
}

func TestUpdateConversationMetadataLocal(t *testing.T) {
	hist := agent.NewHistory(t.TempDir())
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return nil },
		func(string) (config.Model, bool) { return config.Model{}, false }, hist)
	conv := "local-conv-rename"
	if err := hist.SaveState(conv, &cursorpb.ConversationStateStructure{}); err != nil {
		t.Fatal(err)
	}
	var forwarded bool
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		forwarded = true
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	name := "我的对话"
	reqBody, err := proto.Marshal(&cursorpb.UpdateConversationMetadataRequest{
		ConversationId: conv,
		Name:           &name,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://agent.api5.cursor.sh"+updateConversationMetadataPath, bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	s.route(rec, req)
	if forwarded {
		t.Fatal("known local conversation must not forward UpdateConversationMetadata")
	}
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if len(rec.Body.Bytes()) != 0 {
		t.Fatalf("expected empty response, got %v", rec.Body.Bytes())
	}
}

func TestUpdateConversationMetadataOfficialForwards(t *testing.T) {
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return nil },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	var gotPath string
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/proto"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	name := "Official Chat"
	reqBody, _ := proto.Marshal(&cursorpb.UpdateConversationMetadataRequest{
		ConversationId: "official-conv",
		Name:           &name,
	})
	req := httptest.NewRequest(http.MethodPost, "https://agent.api5.cursor.sh"+updateConversationMetadataPath, bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	s.route(rec, req)
	if gotPath != updateConversationMetadataPath {
		t.Fatalf("path=%q", gotPath)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}
