package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipForModelPreservesValidUTF8(t *testing.T) {
	for pad := 0; pad < 6; pad++ {
		raw := strings.Repeat("a", pad) + strings.Repeat("中文输出边界测试", 5000)
		got := ClipForModel(raw)
		if !utf8.ValidString(got) {
			t.Fatalf("pad=%d produced invalid UTF-8 after ClipForModel", pad)
		}
		if len(got) >= len(raw) {
			t.Fatalf("pad=%d was not clipped: %d >= %d", pad, len(got), len(raw))
		}
	}
}
