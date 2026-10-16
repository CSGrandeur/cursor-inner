package provider

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func toolImage(size int) Message {
	return Message{Role: "tool", Content: "Read image file: x.png", Images: []Image{{MIME: "image/png", Data: make([]byte, size)}}}
}

func TestEvictImagesKeepsAllUnderLimits(t *testing.T) {
	var msgs []Message
	for i := 0; i < 18; i++ {
		msgs = append(msgs, toolImage(100))
	}
	out := EvictImages(msgs, config.Model{})
	if n, _ := CountImages(out); n != 18 {
		t.Fatalf("images=%d", n)
	}
}

// 超过张数上限时按批省略最旧的，而不是只省掉超出的那一张（阶梯式，前缀稳定）。
func TestEvictImagesRetiresOldestInBatches(t *testing.T) {
	var msgs []Message
	for i := 0; i < 21; i++ {
		msgs = append(msgs, toolImage(100))
	}
	out := EvictImages(msgs, config.Model{})
	if n, _ := CountImages(out); n != 21-ImageEvictBatch {
		t.Fatalf("images=%d", n)
	}
	if len(out[0].Images) != 0 || !strings.Contains(out[0].Content, ImageOmitted) || !strings.Contains(out[0].Content, "100 字节") {
		t.Fatalf("oldest not replaced: %+v", out[0].Content)
	}
	if len(out[20].Images) != 1 {
		t.Fatal("newest image must stay")
	}
	if len(msgs[0].Images) != 1 {
		t.Fatal("input must not be mutated")
	}
	// 再来一张：仍在同一批次内，省略的条数不变。
	more := append(append([]Message(nil), msgs...), toolImage(100))
	if a, b := retireCount(msgs, 20, DefaultImageBudget), retireCount(more, 20, DefaultImageBudget); a != b {
		t.Fatalf("retire moved %d -> %d", a, b)
	}
}

func TestEvictImagesByteBudgetAndFloor(t *testing.T) {
	var msgs []Message
	for i := 0; i < 5; i++ {
		msgs = append(msgs, toolImage(1_000_000))
	}
	out := EvictImages(msgs, config.Model{ImageBudgetMB: 1})
	if n, _ := CountImages(out); n != ImageFloor {
		t.Fatalf("images=%d want floor %d", n, ImageFloor)
	}
}

func TestUserImagesAreNeverEvicted(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "look", Images: []Image{{MIME: "image/png", Data: make([]byte, 10)}}}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, toolImage(10))
	}
	out, ok := EvictImageBatch(msgs)
	if !ok || len(out[0].Images) != 1 {
		t.Fatalf("ok=%v user images=%d", ok, len(out[0].Images))
	}
	if n, _ := CountImages(out); n != 1+ImageFloor {
		t.Fatalf("images=%d", n)
	}
}

func TestEvictImageBatchStopsAtFloor(t *testing.T) {
	msgs := []Message{toolImage(1), toolImage(1), toolImage(1)}
	if _, ok := EvictImageBatch(msgs); ok {
		t.Fatal("must keep the newest images")
	}
}

func TestImageLimitsFromConfig(t *testing.T) {
	if l, b := ImageLimits(config.Model{MaxImages: 5, ImageBudgetMB: 2}); l != 5 || b != 2_000_000 {
		t.Fatalf("%d %d", l, b)
	}
	if l, b := ImageLimits(config.Model{}); l != DefaultMaxImages || b != DefaultImageBudget {
		t.Fatalf("%d %d", l, b)
	}
}
