package agent

import (
	"strings"
	"testing"
)

// 官方轮次的 tool-result 把 result 写成对象，旧代码整条 root 解析失败，切到自定义模型就整轮报错。
func TestRootWithObjectToolResultDecodes(t *testing.T) {
	raw := []byte(`{"role":"tool","id":"t1","content":[{"type":"tool-result","toolCallId":"c1","toolName":"Read","result":{"contents":"hello","totalLines":1},"providerOptions":{"x":1}}]}`)
	msg, _, err := decodeRoot(raw)
	if err != nil {
		t.Fatalf("decodeRoot: %v", err)
	}
	if msg.ToolCallID != "c1" || !strings.Contains(msg.Content, "hello") {
		t.Fatalf("tool result lost: %+v", msg)
	}
}

func TestRootAssistantWithNonStringTextDecodes(t *testing.T) {
	raw := []byte(`{"role":"assistant","id":"a1","content":[{"type":"text","text":"ok"},{"type":"tool-call","toolCallId":"c1","toolName":"Shell","args":{"command":"ls"}},{"type":"reasoning","text":null}]}`)
	msg, _, err := decodeRoot(raw)
	if err != nil {
		t.Fatalf("decodeRoot: %v", err)
	}
	if msg.Content != "ok" || len(msg.ToolCalls) != 1 {
		t.Fatalf("assistant lost: %+v", msg)
	}
}

func TestRootContentNotArrayStillErrors(t *testing.T) {
	if _, _, err := decodeRoot([]byte(`{"role":"user","content":{"a":1}}`)); err == nil {
		t.Fatal("对象形式的 content 应报错")
	}
}
