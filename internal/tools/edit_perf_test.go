package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// 大文件里找不到 old_string 时，模糊匹配不能逐窗口做字符级 diff 拖死整轮对话。
func TestFuzzyMissOnLargeFileIsFast(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&b, "\tvalue%d := compute(%d, \"label-%d\")\n", i, i, i)
	}
	old := strings.Repeat("\tthis block does not exist anywhere in the file at all\n", 25)
	start := time.Now()
	if _, err := replaceString(args{"old_string": old, "new_string": "x"}, b.String()); err == nil {
		t.Fatal("matched a block that is not there")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("fuzzy miss took %v", d)
	}
}
