package tunnel

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func mustConfig(t *testing.T) map[string]any {
	t.Helper()
	b, err := Config(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080, ProxyType: "socks",
		DNSUpstreams: []string{"192.0.2.53", "198.51.100.53", "127.0.0.1"}})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return m
}

func TestConfigShape(t *testing.T) {
	js, _ := json.Marshal(mustConfig(t))
	s := string(js)
	// 不能有默认全局路由，否则所有流量都会进 TUN（用户明确不要）。
	if strings.Contains(s, "0.0.0.0/0") || strings.Contains(s, `"::/0"`) {
		t.Fatal("配置里不应出现全局路由")
	}
	for _, suf := range OfficialSuffixes {
		if !strings.Contains(s, "."+suf) {
			t.Fatalf("缺少官方后缀 %s", suf)
		}
	}
	// GitHub/Google 等第三方不该被强制走代理。
	if strings.Contains(s, "github.com") || strings.Contains(s, "google") {
		t.Fatal("第三方域名不应出现在配置里")
	}
}

func TestConfigMatchesApexAndSub(t *testing.T) {
	m := mustConfig(t)
	dnsRule := m["dns"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	dom, _ := json.Marshal(dnsRule["domain"])
	suf, _ := json.Marshal(dnsRule["domain_suffix"])
	// 顶点域名要精确匹配（domain），子域名要后缀匹配（domain_suffix）。只有后缀会漏掉裸顶点。
	if !strings.Contains(string(dom), `"grok.com"`) || !strings.Contains(string(dom), `"x.ai"`) {
		t.Fatalf("DNS 规则缺少顶点域名精确匹配：%s", dom)
	}
	if !strings.Contains(string(suf), `".grok.com"`) {
		t.Fatalf("DNS 规则缺少子域名后缀匹配：%s", suf)
	}
}

func TestConfigRouteAddress(t *testing.T) {
	in := mustConfig(t)["inbounds"].([]any)[0].(map[string]any)
	ra, _ := json.Marshal(in["route_address"])
	s := string(ra)
	for _, want := range []string{"198.18.0.0/15", "fc00::/18", "192.0.2.53/32", "198.51.100.53/32"} {
		if !strings.Contains(s, want) {
			t.Fatalf("route_address 缺少 %s：%s", want, s)
		}
	}
	// 回环 DNS 不应被路由进 TUN（否则可能成环）。
	if strings.Contains(s, "127.0.0.1") {
		t.Fatalf("route_address 不应含回环地址：%s", s)
	}
}

func TestConfigHTTPProxy(t *testing.T) {
	b, err := Config(Options{ProxyHost: "127.0.0.1", ProxyPort: 3128, ProxyType: "http",
		DNSUpstreams: []string{"223.5.5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type": "http"`) {
		t.Fatal("应生成 http 出站")
	}
}

func TestConfigErrors(t *testing.T) {
	// 代理无效。
	if _, err := Config(Options{ProxyHost: "", ProxyPort: 0, DNSUpstreams: []string{"8.8.8.8"}}); err == nil {
		t.Fatal("空代理应报错")
	}
	// 无可路由 DNS（只有回环）应报错，让调用方回退。
	if _, err := Config(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080, DNSUpstreams: []string{"127.0.0.1", "::1"}}); err == nil {
		t.Fatal("只有回环 DNS 时应报错")
	}
	// 完全没有 DNS 也应报错。
	if _, err := Config(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080}); err == nil {
		t.Fatal("无 DNS 应报错")
	}
}

// TestConfigSingBoxCheck 只有在环境变量 TUNNEL_SINGBOX 指向 sing-box 可执行文件时才跑，
// 用真正的 sing-box 校验配置 schema；CI 默认跳过，保持确定性。
func TestConfigSingBoxCheck(t *testing.T) {
	bin := os.Getenv("TUNNEL_SINGBOX")
	if bin == "" {
		t.Skip("设置 TUNNEL_SINGBOX 指向 sing-box 可执行文件以启用本测试")
	}
	b, err := Config(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080, ProxyType: "socks",
		DNSUpstreams: []string{"192.0.2.53"}, LogPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.CreateTemp(t.TempDir(), "cfg*.json")
	f.Write(b)
	f.Close()
	out, err := exec.Command(bin, "check", "-c", f.Name()).CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box check 失败：%v\n%s", err, out)
	}
}

// TUN 与 Grok bypass 一致：路由只纳入 fake-ip 段（官方流量进代理）与真实 DNS，
// 绝不纳入 LAN/WSL 私有网段——这些由 Grok 的 --proxy-bypass-list 负责直连，不进 TUN。
func TestRouteAddressFakeIPNotPrivate(t *testing.T) {
	raw, err := Config(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080, DNSUpstreams: []string{"192.0.2.53"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "198.18.0.0/15") {
		t.Fatal("TUN must route the fake-ip range 198.18.0.0/15 so official traffic reaches the proxy")
	}
	for _, p := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"} {
		if strings.Contains(s, p) {
			t.Errorf("TUN config must not route private range %s (LAN/WSL stays direct)", p)
		}
	}
}
