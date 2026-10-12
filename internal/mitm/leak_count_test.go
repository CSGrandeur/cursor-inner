package mitm

import (
	"testing"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

// 托盘的泄漏告警看 Traffic().Leaks：只数真正带出去的请求，遥测类（会被改写掉）不算。
func TestTrafficCountsCustomModelLeaks(t *testing.T) {
	const id = "model-12345678"
	s := New(
		func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: id, DisplayName: "Mine"}} },
		func(string) (config.Model, bool) { return config.Model{}, false },
		agent.NewHistory(t.TempDir()),
	)
	s.warnCustomModel("/aiserver.v1.AnalyticsService/Batch", []byte(id), "")
	s.warnCustomModel("/aiserver.v1.AiService/StreamChat", []byte("nothing here"), "")
	if tr := s.Traffic(); tr.Leaks != 0 {
		t.Fatalf("quiet paths and clean bodies must not count: %+v", tr)
	}
	s.warnCustomModel("/aiserver.v1.AiService/StreamChat", []byte("x"+id+"y"), "")
	if tr := s.Traffic(); tr.Leaks != 1 || tr.LastLeak != "Mine" {
		t.Fatalf("leak not counted: %+v", tr)
	}
}
