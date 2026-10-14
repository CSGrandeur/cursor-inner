package mitm

import (
	"testing"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
)

func heartbeatFrame(id string) []byte {
	body := protox.AppendString(nil, 1, "3a00")
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, id))
	body = protox.AppendVarint(body, 3, 1)
	return protox.Frame(0, body)
}

// 0.5.11 开发者版把官方 run 的心跳也在本地回 200：官方收不到心跳，官方模型做一步就停。
func TestHeartbeatOfOfficialRunIsForwarded(t *testing.T) {
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: "mine"}} },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	route, ok := s.heartbeatRoute(heartbeatFrame("50ef8023-8b46-4828-bd6b-084b7fe8f763"))
	if !ok {
		t.Fatal("应识别为心跳")
	}
	if route.Local {
		t.Fatal("不是本机会话的心跳必须转发官方，不能本地回 200")
	}
}
