package agent

import (
	"context"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

// 热替换/重启会丢掉内存里的 finished 表。答完的 run id 必须还能从磁盘历史接回，
// 否则客户端约 90 秒后的 resume（含后台任务结束后的 resume）会转给官方或只回
// turn_ended，弹出 Error resuming chat: A background task finished...
func TestNoteResumeSurvivesHubRestart(t *testing.T) {
	dir := t.TempDir()
	fm := &fakeModel{responses: []string{sse(`{"choices":[{"delta":{"content":"done"}}]}`)}}
	srv := fm.server(t)
	model := config.Model{ID: "mine", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	hub := New(func(string) (config.Model, bool) { return model, true }, NewHistory(dir))
	if _, err := hub.Bidi(bidi(t, "run-hot", runRequest("mine", "conv-hot", "finish", &cursorpb.RequestContextEnv{}))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := hub.Wait(ctx, "run-hot")
	if err != nil || session == nil {
		t.Fatalf("wait: %v", err)
	}
	if err := session.Run(ctx, dialer.Direct(), func(*cursorpb.AgentServerMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	hub.Done("run-hot")

	// 模拟热交接：新进程、同一数据目录，内存 finished 表为空。
	hub2 := New(func(string) (config.Model, bool) { return model, true }, NewHistory(dir))
	if hub2.Finished("run-hot") {
		t.Fatal("新 Hub 不应还带着旧进程的内存 finished")
	}
	if !hub2.NoteResume("run-hot", "run-hot-resume") {
		t.Fatal("热交接后仍应按磁盘记录接回本机答完的 run")
	}
	got, err := hub2.Wait(ctx, "run-hot-resume")
	if err != nil || got == nil {
		t.Fatalf("resume wait: %v session=%v", err, got)
	}
	if !got.Finished() {
		t.Fatal("接回的会话应已结束，走回放而不是再调模型")
	}
	frames, err := got.Replay()
	if err != nil {
		t.Fatal(err)
	}
	blobs, checkpoint := 0, 0
	for _, m := range frames {
		if kv := m.GetKvServerMessage(); kv != nil && kv.GetSetBlobArgs() != nil {
			blobs++
		}
		if m.GetConversationCheckpointUpdate() != nil {
			checkpoint++
		}
	}
	if blobs == 0 || checkpoint != 1 {
		t.Fatalf("热交接后回放不完整 blobs=%d checkpoint=%d", blobs, checkpoint)
	}
}
