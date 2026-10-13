package mitm

import (
	"testing"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

// 目录 id 出现在非对话请求里只记调试日志，不计入托盘告警。
// 对话没留在本机是另一条错误，不从这里计数。
func TestTrafficCountsCustomModelLeaks(t *testing.T) {
	const id = "model-12345678"
	s := New(
		func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: id, DisplayName: "Mine"}} },
		func(string) (config.Model, bool) { return config.Model{}, false },
		agent.NewHistory(t.TempDir()),
	)
	s.warnCustomModel("/aiserver.v1.AnalyticsService/Batch", []byte(id), "")
	s.warnCustomModel("/aiserver.v1.AiService/StreamChat", []byte("x"+id+"y"), "")
	if tr := s.Traffic(); tr.Leaks != 0 || tr.LastLeak != "" {
		t.Fatalf("model id in a forwarded request is not a tray alert: %+v", tr)
	}
}
