package connlog

import (
	"context"
	"testing"
)

func TestNoteRecordsLastRoute(t *testing.T) {
	ctx, n := With(context.Background())
	if n.Route() != "" {
		t.Fatalf("初始应为空，得到 %q", n.Route())
	}
	Set(ctx, "direct")
	Set(context.WithoutCancel(ctx), "proxy")
	if n.Route() != "proxy" {
		t.Fatalf("应以最后一次为准，得到 %q", n.Route())
	}
	Set(context.Background(), "x") // 没有记录不应 panic
	var nilNote *Note
	if nilNote.Route() != "" {
		t.Fatal("nil 记录应返回空")
	}
}
