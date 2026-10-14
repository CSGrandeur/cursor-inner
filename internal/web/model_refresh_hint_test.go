package web

import (
	"os"
	"strings"
	"testing"
)

// 新增模型后要提示：Cursor 按自己的节奏拉模型列表（聚焦约 5 分钟），以及立即刷新的办法。
// 编辑已有模型不提示。
func TestAddModelShowsCursorRefreshHint(t *testing.T) {
	raw, err := os.ReadFile("page.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	post := strings.Index(src, `await send("/api/models", {method: "POST"`)
	if post < 0 {
		t.Fatal("add-model POST not found")
	}
	tail := src[post : post+300]
	if !strings.Contains(tail, `if (!editing) toast(t("modelAddedRefresh"), "ok");`) {
		t.Fatalf("add-model success must toast the refresh hint, got:\n%s", tail)
	}
	for _, want := range []string{"Refresh model list", "Reload Window", "5 "} {
		if strings.Count(src, want) < 2 {
			t.Fatalf("hint must mention %q in both languages", want)
		}
	}
}
