package grokbot

import (
	"net"
	"net/netip"
	"os"
	"strings"

	"cursor-inner/internal/official"
)

// EnvProxyMark 出现在命令行里，表示这次启动已带上 undici 认的环境代理开关。
// 旧进程只有 --proxy-server 和 --disable-quic 时 Routed 为假，ShouldRestart 会重开。
const EnvProxyMark = "--use-env-proxy"

// privateCIDRs 是 LAN / WSL / 链路本地等私有网段。Grok 的直连流量落在这些网段时不走代理，
// 本机服务、局域网、WSL 都能自然可达。绝不包含 TUN 的 fake-ip 段（198.18.0.0/15）或任何
// 全量通配符：那会让官方域名（经 fake-ip 捕获）绕过代理直连而泄漏。
var privateCIDRs = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"192.0.0.0/24",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"fc00::/7",
	"fe80::/10",
}

// LoopbackBypass 是 Chromium --proxy-bypass-list 的值：回环 + 本机容器桥域名 + LAN/WSL 私有网段。
// 不加 <-loopback>（会把 localhost 也塞进代理），不加 fake-ip 段或全量通配符。
// 保持静态常量，避免网卡地址变化触发 Routed / ShouldRestart 误判重开。
var LoopbackBypass = strings.Join(append([]string{
	"localhost", "*.localhost", "127.0.0.1", "[::1]", "<local>", "*.local", "wsl.localhost",
	"host.docker.internal", "gateway.docker.internal", "kubernetes.docker.internal",
}, privateCIDRs...), ";")

// LoopbackNoProxy 是写进 NO_PROXY/no_proxy 的静态基础列表：回环 + 本机容器桥域名 + LAN/WSL 私有网段
// （本机主机名与活动内网网卡地址在 ProxyEnv 里追加，兼容不解析 CIDR 的 proxy-from-env、undici 与 urllib）。
// 同时写 ::1 与 [::1]：proxy-from-env 会把裸 ::1 按 host:port 拆坏，只认带方括号的 [::1]。
// 不继承进程、用户级或系统级 NO_PROXY：v2rayN 等在 PAC 模式下常留下宽泛或
// 过时的 no_proxy，undici 会按 DIRECT 出门，表现为云功能异常（例如误报额度用尽）。
var LoopbackNoProxy = strings.Join(append([]string{
	"localhost", ".localhost", "127.0.0.1", "::1", "[::1]", ".local", "wsl.localhost",
	"host.docker.internal", "gateway.docker.internal", "kubernetes.docker.internal",
}, privateCIDRs...), ",")

// localBridgeIPs 是 Linux 子系统与容器默认网桥常见的本机侧保留地址；Windows 宿主网卡枚举看不到它们，
// 但子进程里的 urllib / proxy-from-env / undici 不解析 CIDR，直连这些地址时需要字面量匹配。
// 全部严格落在 RFC 1918 私网段内，绝不含 fake-ip（198.18.0.0/15、fc00::/18）。
var localBridgeIPs = []string{
	"10.255.255.254",
	"172.17.0.1",
}

// tunInternalULA 是本程序仅官方 TUN 网卡自用的 ULA 前缀（见 tunnel.Config），不放进 NO_PROXY。
var tunInternalULA = netip.MustParsePrefix("fdfe:dcba:9876::/126")

// interfaceAddrs 供测试注入网卡地址；进程内直接走系统调用（Windows 为 GetAdaptersAddresses），不起子进程。
var interfaceAddrs = net.InterfaceAddrs

// WebRTCPolicy 让 WebRTC 不走代理时就不发 UDP：否则语音/实时通话的 UDP 会绕过 HTTP 代理直连，
// 还会暴露本机真实出口 IP。
const WebRTCPolicy = "--force-webrtc-ip-handling-policy=disable_non_proxied_udp"

// ProxyArgs 返回接管 Grok 时要加的启动参数。proxyURL 为空时返回 nil。
//
// 固定走 --proxy-server，不设 --proxy-pac-url / --proxy-auto-detect，也绝不加
// --no-proxy-server（它会盖掉 --proxy-server，流量改跟系统 PAC/直连）。
// --proxy-bypass-list 为回环 + LAN/WSL 私有网段（绝不含 fake-ip 段或全量通配符）。
// 不要加 <-loopback>：那会强迫 localhost 也进代理，本地服务反而连不上。
func ProxyArgs(proxyURL string) []string {
	if proxyURL == "" {
		return nil
	}
	return []string{
		"--proxy-server=" + proxyURL,
		"--disable-quic",
		EnvProxyMark,
		"--proxy-bypass-list=" + LoopbackBypass,
		WebRTCPolicy,
	}
}

// ProxyEnv 在 base 环境上写好 Grok Bot 走 HTTP 代理所需的变量。
// Electron 窗口认 --proxy-server；主进程 undici 要 HTTPS_PROXY，且默认不读，
// 除非 NODE_USE_ENV_PROXY=1（或等价的 --use-env-proxy）。
// NO_PROXY/no_proxy 写回环 + LAN/WSL 私有网段，外加本机主机名与本机私网网卡字面量；gRPC 客户端认 grpc_proxy。
func ProxyEnv(proxyURL string, base []string) []string {
	out := make([]string, 0, len(base)+16)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "ELECTRON_RUN_AS_NODE",
			"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
			"GRPC_PROXY", "NODE_USE_ENV_PROXY":
			continue
		}
		out = append(out, entry)
	}
	if proxyURL == "" {
		return out
	}
	for _, key := range []string{
		"http_proxy", "https_proxy", "all_proxy", "grpc_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "GRPC_PROXY",
	} {
		out = append(out, key+"="+proxyURL)
	}
	noProxy := buildNoProxy()
	out = append(out,
		"no_proxy="+noProxy,
		"NO_PROXY="+noProxy,
		"NODE_USE_ENV_PROXY=1",
	)
	return out
}

func buildNoProxy() string {
	base := strings.Split(LoopbackNoProxy, ",")
	seen := make(map[string]bool, len(base)+16)
	out := make([]string, 0, len(base)+16)
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || strings.ContainsAny(tok, ", \t\r\n") {
			return
		}
		key := strings.ToLower(tok)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, tok)
	}
	for _, tok := range base {
		add(tok)
	}
	if h, err := os.Hostname(); err == nil {
		add(h)
	}
	if addrs, err := interfaceAddrs(); err == nil {
		for _, a := range addrs {
			ip, ok := interfaceIP(a)
			if !ok || !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || !official.Reserved(ip) {
				continue
			}
			if ip.Is4() {
				add(ip.String())
				continue
			}
			if ip.IsLinkLocalUnicast() || tunInternalULA.Contains(ip) {
				continue
			}
			s := ip.WithZone("").String()
			add(s)
			add("[" + s + "]")
		}
	}
	for _, lit := range localBridgeIPs {
		if ip, err := netip.ParseAddr(lit); err == nil && official.Reserved(ip) {
			add(ip.String())
		}
	}
	return strings.Join(out, ",")
}

func interfaceIP(a net.Addr) (netip.Addr, bool) {
	switch v := a.(type) {
	case *net.IPNet:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip.Unmap(), true
		}
	case *net.IPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip.Unmap(), true
		}
	}
	return netip.Addr{}, false
}
