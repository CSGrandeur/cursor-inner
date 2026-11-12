package agent

import (
	"testing"

	"cursor-inner/internal/provider"
)

// 同用户文本、同步数但内容不同的轮不能复用旧轮：否则客户端重绘时新输出整段消失
// （2026-11-12 用户报告「下一轮对话时上一轮 agent 输出很多不见了」）。
func TestTurnReuseRequiresIdenticalContent(t *testing.T) {
	old := []provider.Message{
		{Role: "user", Content: "继续"},
		{Role: "assistant", Content: "old answer"},
	}
	a, err := writeCheckpoint("", "req1", old, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, b := range a.Blobs {
		blobs[string(b.ID)] = b.Data
	}
	fresh := []provider.Message{
		{Role: "user", Content: "继续"},
		{Role: "assistant", Content: "new answer"},
	}
	b, err := writeCheckpoint("", "req2", fresh, 0, 0, 0, "", 0, nil, a.State, blobs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b2 := range b.Blobs {
		blobs[string(b2.ID)] = b2.Data
	}
	steps := turnSteps(t, blobs, b.State.GetTurns()[0])
	if len(steps) != 1 || steps[0] != "assistant:new answer" {
		t.Fatalf("new content must survive, got %v", steps)
	}
}

// 内容完全一致的轮仍要复用：检查点稳定，不重复造 blob。
func TestTurnReuseIdenticalContentReuses(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Reasoning: "think", Content: "hello"},
	}
	a, err := writeCheckpoint("", "req1", msgs, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, b := range a.Blobs {
		blobs[string(b.ID)] = b.Data
	}
	b, err := writeCheckpoint("", "req2", msgs, 0, 0, 0, "", 0, nil, a.State, blobs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(b.State.GetTurns()[0]) != string(a.State.GetTurns()[0]) {
		t.Fatal("identical turn must reuse the previous turn id")
	}
}

// 旧轮步骤 blob 缺失时不能复用半份历史，必须重建出完整内容。
func TestTurnReuseMissingStepBlobRebuilds(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	a, err := writeCheckpoint("", "req1", msgs, 0, 0, 0, "", 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	for _, b := range a.Blobs {
		blobs[string(b.ID)] = b.Data
	}
	// 删掉一个步骤 blob，模拟旧检查点损坏。
	for id := range blobs {
		delete(blobs, id)
		break
	}
	b, err := writeCheckpoint("", "req2", msgs, 0, 0, 0, "", 0, nil, a.State, blobs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b2 := range b.Blobs {
		blobs[string(b2.ID)] = b2.Data
	}
	steps := turnSteps(t, blobs, b.State.GetTurns()[0])
	if len(steps) != 1 || steps[0] != "assistant:hello" {
		t.Fatalf("rebuild must restore full content, got %v", steps)
	}
}
