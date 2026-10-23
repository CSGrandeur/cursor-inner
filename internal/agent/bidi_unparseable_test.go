package agent

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
)

// 读不成 AgentClientMessage 的 BidiAppend 不能被认成本地回合（635a915 的“挽救”把 Cursor 3.26 本地循环的
// InferenceClientMessage 认成本地回合并吞掉，客户端一直等）。
func TestBidiDoesNotClaimUnparseableMessage(t *testing.T) {
	const id = "62c2ab93e935104a"
	var inner []byte
	inner = protowire.AppendTag(inner, 9, protowire.BytesType)
	inner = protowire.AppendBytes(inner, protox.AppendString(nil, 1, id))
	inner = protowire.AppendTag(inner, 50, protowire.EndGroupType)
	client := protox.AppendBytes(nil, 1, inner)
	for _, binary := range []bool{false, true} {
		var body []byte
		if binary {
			body = protox.AppendBytes(nil, 4, client)
		} else {
			body = protox.AppendString(nil, 1, hex.EncodeToString(client))
		}
		body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, "req"))
		h := New(func(string) (config.Model, bool) { return config.Model{ID: id}, true }, nil)
		route, err := h.Bidi(body)
		if err == nil || route.Local {
			t.Fatalf("binary=%v: route=%+v err=%v, want parse error and not local", binary, route, err)
		}
	}
}
