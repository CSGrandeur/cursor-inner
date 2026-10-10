package tunnel

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"

	"cursor-inner/internal/i18n"
)

// Options 是生成 sing-box 配置所需的输入。
type Options struct {
	ProxyType    string   // "socks" 或 "http"；留空按 socks
	ProxyHost    string   // 代理主机，通常 127.0.0.1
	ProxyPort    int      // 代理端口，如 1080
	DNSUpstreams []string // 用户当前真实 DNS（IP）。为空则无法生成：没有它就没法在不改变其它域名解析的前提下工作
	ProxyUser    string   // 可选，代理用户名（socks/http 认证，sing-box 原生支持）
	ProxyPass    string   // 可选，代理密码
	LogPath      string   // 可选，sing-box 日志文件
	FakeIP4      string   // 默认 198.18.0.0/15
	FakeIP6      string   // 默认 fc00::/18
}

const (
	defaultFakeIP4 = "198.18.0.0/15"
	defaultFakeIP6 = "fc00::/18"
)

// Config 生成 sing-box 配置：
//   - 只有官方域名经 fake-ip 落入 198.18/fc00 段，再由 route 规则交给代理；
//   - 其它域名用用户真实 DNS 原样解析、直连，连接不进 TUN；
//   - route_address 只含 fake-ip 段和 DNS 服务器 IP，所以只有「官方连接」和「DNS」进 TUN，别的流量不受影响；
//   - sniff + 按 ip_cidr(fake 段) 兜底，覆盖写死 IP / DoH 情况下仍把官方流量送进代理；
//   - 代理出站拨号经 direct 的真实网卡，不回环进 TUN，避免成环；v2rayN 自身到上游的连接是真实 IP，不在捕获段内。
func Config(o Options) ([]byte, error) {
	host := strings.TrimSpace(o.ProxyHost)
	if host == "" || o.ProxyPort <= 0 || o.ProxyPort > 65535 {
		return nil, i18n.E("代理地址无效，无法生成 TUN 配置", "Invalid proxy address for the TUN config")
	}
	ptype := o.ProxyType
	if ptype == "" {
		ptype = "socks"
	}
	if ptype != "socks" && ptype != "http" {
		return nil, fmt.Errorf("unsupported proxy type %q", ptype)
	}
	fake4, fake6 := o.FakeIP4, o.FakeIP6
	if fake4 == "" {
		fake4 = defaultFakeIP4
	}
	if fake6 == "" {
		fake6 = defaultFakeIP6
	}

	ups, routeDNS := normalizeDNS(o.DNSUpstreams)
	if len(ups) == 0 || len(routeDNS) == 0 {
		return nil, i18n.E("没有检测到可路由的真实 DNS，TUN 无法在不影响其它域名的前提下启动", "No routable real DNS server detected; the TUN cannot start without affecting other domains")
	}

	// 子域名用 domain_suffix(".grok.com")，顶点域名用 domain("grok.com") 精确匹配——
	// 只有后缀会漏掉裸顶点域名（grok.com 本身不是 .grok.com 的子域），两者都要。
	apex := append([]string(nil), OfficialSuffixes...)
	suffixes := make([]string, 0, len(OfficialSuffixes))
	for _, s := range OfficialSuffixes {
		suffixes = append(suffixes, "."+s)
	}

	dnsServers := []any{
		map[string]any{"type": "fakeip", "tag": "dns-fake", "inet4_range": fake4, "inet6_range": fake6},
	}
	for i, ip := range ups {
		dnsServers = append(dnsServers, map[string]any{
			"type": "udp", "tag": fmt.Sprintf("dns-local-%d", i), "server": ip,
		})
	}
	localTag := "dns-local-0"

	proxyOut := map[string]any{"type": ptype, "tag": "proxy", "server": host, "server_port": o.ProxyPort}
	if ptype == "socks" {
		proxyOut["version"] = "5"
	}
	if o.ProxyUser != "" {
		proxyOut["username"] = o.ProxyUser
		proxyOut["password"] = o.ProxyPass
	}

	routeAddress := []string{fake4, fake6}
	routeAddress = append(routeAddress, routeDNS...)

	cfg := map[string]any{
		"log": logBlock(o.LogPath),
		"dns": map[string]any{
			"servers": dnsServers,
			"rules": []any{
				map[string]any{"domain": apex, "domain_suffix": suffixes, "server": "dns-fake"},
			},
			"final":    localTag,
			"strategy": "prefer_ipv4",
		},
		"inbounds": []any{
			map[string]any{
				"type":          "tun",
				"tag":           "tun-in",
				"address":       []string{"192.0.2.1/30", "fdfe:dcba:9876::1/126"},
				"mtu":           1500,
				"auto_route":    true,
				"strict_route":  false,
				"route_address": routeAddress,
				"stack":         "gvisor",
			},
		},
		"outbounds": []any{
			proxyOut,
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"rules": []any{
				map[string]any{"action": "sniff"},
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
				map[string]any{"inbound": "tun-in", "ip_cidr": []string{fake4, fake6}, "outbound": "proxy"},
				map[string]any{"domain": apex, "domain_suffix": suffixes, "outbound": "proxy"},
			},
			"final":                   "direct",
			"auto_detect_interface":   true,
			"default_domain_resolver": map[string]any{"server": localTag},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func logBlock(path string) map[string]any {
	b := map[string]any{"level": "warn", "timestamp": true}
	if strings.TrimSpace(path) != "" {
		b["output"] = path
	}
	return b
}

// normalizeDNS 去重、剔除回环与无效项，并给每个上游算出要放进 route_address 的单主机网段。
// 回环 DNS（127/::1，常是本机 DoH 代理）不加进 route_address：它本来就不进 TUN，加了反而可能成环。
func normalizeDNS(in []string) (servers []string, routes []string) {
	seen := map[string]bool{}
	for _, raw := range in {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		servers = append(servers, ip.String())
		if ip.IsLoopback() {
			continue
		}
		if ip.To4() != nil {
			routes = append(routes, ip.String()+"/32")
		} else {
			routes = append(routes, ip.String()+"/128")
		}
	}
	sort.Strings(servers)
	sort.Strings(routes)
	return servers, routes
}
