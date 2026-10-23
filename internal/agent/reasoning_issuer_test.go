package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

const anthropicOK = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"SIG-B\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// 备用模型接手时，不能把主模型签发的 thinking 签名交给它：对方拒收，坏块留在历史里会让之后每一轮都失败。
// 主模型自己重试时签名要原样保留；备用模型的回答要记上它自己是签发方。
func TestFallbackDoesNotReplayForeignReasoningSignature(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies[body.Model] = string(raw)
		mu.Unlock()
		if body.Model == "primary" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicOK)
	}))
	defer srv.Close()
	mk := func(name string) config.Model {
		m, err := provider.Prepare(config.Model{DisplayName: name, Type: "anthropic", BaseURL: srv.URL + "/v1", APIKey: "k", Model: name, Reasoning: true})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	backup := mk("backup")
	primary := mk("primary")
	primary.Fallback = []string{backup.ID}
	history := []provider.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1", Reasoning: "plain thought", ReasoningSignature: "SIG-A", ReasoningBy: provider.ReasoningIssuer(primary)},
		{Role: "user", Content: "q2"},
	}
	used, reply, err := chatWithFallback(context.Background(), primary, []config.Model{primary, backup},
		func(config.Model) (dialer.Func, error) { return dialer.Direct(), nil },
		"sys", history, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if used.ID != backup.ID {
		t.Fatalf("used %s", used.DisplayName)
	}
	if !strings.Contains(bodies["primary"], "SIG-A") {
		t.Fatalf("primary must still get its own signature: %s", bodies["primary"])
	}
	if strings.Contains(bodies["backup"], "SIG-A") {
		t.Fatalf("backup received the primary's signature: %s", bodies["backup"])
	}
	if history[1].ReasoningSignature != "SIG-A" {
		t.Fatal("stripping must not mutate the caller's history")
	}
	if reply.ReasoningSignature != "SIG-B" || reply.ReasoningBy != provider.ReasoningIssuer(backup) {
		t.Fatalf("reply not stamped by backup: sig=%q by=%q", reply.ReasoningSignature, reply.ReasoningBy)
	}
}

// 签发方要随检查点保存和读回，否则续聊后就分不清该不该去掉签名。
func TestCheckpointKeepsReasoningIssuer(t *testing.T) {
	m := provider.Message{Role: "assistant", Content: "a", Reasoning: "r", ReasoningSignature: "S", ReasoningBy: "anthropic|https://x/v1|m"}
	doc := wireMessage(m, 0, nil)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"signedBy":"anthropic|https://x/v1|m"`) {
		t.Fatalf("issuer not written: %s", raw)
	}
}
