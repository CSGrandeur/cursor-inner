package tools

import (
	"strings"
	"testing"

	"cursor-inner/internal/provider"
)

func TestDynamicToolNamesMapToMCP(t *testing.T) {
	got := Canonical(provider.ToolCall{ID: "1", Name: "GetDynamicTools", Arguments: `{"namespace":"github","pattern":"issue"}`})
	if got.Name != "GetMcpTools" || !strings.Contains(got.Arguments, `"server":"github"`) || strings.Contains(got.Arguments, "namespace") {
		t.Fatalf("GetDynamicTools -> %+v", got)
	}
	got = Canonical(provider.ToolCall{ID: "2", Name: "CallDynamicTool", Arguments: `{"namespace":"github","toolName":"get_issue","arguments":{"n":1}}`})
	if got.Name != "CallMcpTool" || !strings.Contains(got.Arguments, `"server":"github"`) || !strings.Contains(got.Arguments, `"toolName":"get_issue"`) {
		t.Fatalf("CallDynamicTool -> %+v", got)
	}
	// 改名后由本机的 MCP 列表处理，不再落到「is not available」。
	if _, _, _, handled := ListMCP(Canonical(provider.ToolCall{ID: "3", Name: "GetDynamicTools", Arguments: `{}`}), nil); !handled {
		t.Fatal("GetDynamicTools should be answered by the local MCP listing")
	}
	same := provider.ToolCall{ID: "4", Name: "Read", Arguments: `{"path":"a"}`}
	if Canonical(same) != same {
		t.Fatal("other tools must stay unchanged")
	}
}
