package provider

import (
	"context"
	"testing"

	"cursor-inner/internal/runlog"
)

func TestAttachRequestDeltaIsCountOnly(t *testing.T) {
	const id = "delta-test-1"
	lastRequestDigest.Delete(id)
	ctx := runlog.WithRequest(context.Background(), id)
	first := attachRequestDelta(ctx, runlog.Note{Messages: 247, Chars: 309807, Bytes: 512803}, false)
	if first.DeltaMessages != 0 || first.DeltaRunes != 0 || first.DeltaBytes != 0 {
		t.Fatalf("first request should have no delta: %+v", first)
	}
	lastRequestDigest.Store(id, storeRequestDigest(first))
	second := attachRequestDelta(ctx, runlog.Note{Messages: 249, Chars: 309837, Bytes: 513347}, false)
	if second.DeltaMessages != 2 || second.DeltaRunes != 30 || second.DeltaBytes != 544 {
		t.Fatalf("delta %+v", second)
	}
	lastRequestDigest.Store(id, storeRequestDigest(second))
	errNote := copyRequestDelta(ctx, runlog.Note{Kind: "model_error", Messages: 249, Chars: 309837, Bytes: 513347})
	if errNote.DeltaMessages != 2 || errNote.DeltaRunes != 30 || errNote.DeltaBytes != 544 {
		t.Fatalf("error should copy request delta: %+v", errNote)
	}
}
