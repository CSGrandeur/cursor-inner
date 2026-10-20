// Package official 是官方端点清单，以及“代理范围：仅官方”时按目标决定走代理还是直连的规则。
//
// 先有名字，再对名单。名单里的域名、TUN 的 fake-ip 段、没有域名的公网 IP 走代理；
// 回环、LAN/WSL 私网，以及名单以外的域名直连。协议本身不决定走法。
package official

import (
	"net/netip"
	"strings"
)

// Suffixes 是必须经代理的官方域名后缀，与 scripts/grok-leak-check.ps1、strict-egress.ps1 保持一致。
var Suffixes = []string{
	"grok.com",
	"x.ai",
	"spacexai.com",
	"cursor.com",
	"cursor.sh",
	"cursorapi.com",
	"cursor-cdn.com",
	"cursorvm.com",
	"anysphere.co",
	// OpenAI 帮助中心里列出的第一方后缀。Stripe、Sentry、Intercom 这类第三方不在名单里。
	"openai.com",
	"chatgpt.com",
	"oaistatic.com",
	"oaiusercontent.com",
	"oaistatsig.com",
}

// alsoProxy 是用来解析官方域名的 DoH 服务。严格模式和泄漏检查脚本经 Grok 的代理地址查询它们，
// “仅官方”时也走代理：直连可能被污染或不通，而且查询内容就是官方域名。
var alsoProxy = []string{
	"cloudflare-dns.com",
	"dns.google",
}

// TUN 的 fake-ip 段。落在这里的地址就是官方域名，永远走代理。
const (
	FakeIP4 = "198.18.0.0/15"
	FakeIP6 = "fc00::/18"
)

var (
	fake4 = netip.MustParsePrefix(FakeIP4)
	fake6 = netip.MustParsePrefix(FakeIP6)
	// 与 grokbot 的回环/私网绕过一致：IPv4 内网预留段（不含 fake-ip 段）、LAN、WSL、链路本地。
	private = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
	}
)

// Route 是一个目标该怎么走。
type Route int

const (
	// Proxy：走配置的代理。
	Proxy Route = iota
	// Direct：直连。
	Direct
	// Inspect：拨号目标不是官方域名，先看客户端第一段数据里的 TLS SNI 或 HTTP Host。
	Inspect
	// ByDial：这段数据里没有主机名。用拨号目标上的名字决定，不因为协议认不出来改走代理。
	ByDial
)

func (r Route) String() string {
	switch r {
	case Direct:
		return "direct"
	case Inspect:
		return "inspect"
	case ByDial:
		return "bydial"
	}
	return "proxy"
}

func normalize(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return strings.TrimSuffix(host, ".")
}

func matchSuffix(host string, list []string) bool {
	for _, s := range list {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// Name 报告 host 是不是官方域名（含子域名）。
func Name(host string) bool {
	return matchSuffix(normalize(host), Suffixes)
}

// MustProxy 报告这个名字是否必须走代理：官方域名或解析官方域名用的 DoH。
// 这类名字一律不在本地解析、不按解析结果直连，防 DNS 重绑定把官方流量引到直连。
func MustProxy(host string) bool {
	h := normalize(host)
	return matchSuffix(h, Suffixes) || matchSuffix(h, alsoProxy)
}

// Reserved 报告 ip 是否属于直连的内网预留段（回环、未指定、私网、CGNAT、链路本地等）。
// TUN 的 fake-ip 段不算：落在那里的就是官方域名。
func Reserved(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || fake4.Contains(ip) || fake6.Contains(ip) {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	for _, p := range private {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ForHost 是“仅官方”时一个目标主机（不带端口）的走法。
func ForHost(host string) Route {
	h := normalize(host)
	if h == "" {
		return Proxy
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		ip = ip.Unmap()
		if fake4.Contains(ip) || fake6.Contains(ip) {
			return Proxy
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			return Direct
		}
		for _, p := range private {
			if p.Contains(ip) {
				return Direct
			}
		}
		// 公网 IP 没有域名，对不上名单。官方客户端有时直接连 IP，所以走代理。
		return Proxy
	}
	if matchSuffix(h, Suffixes) || matchSuffix(h, alsoProxy) {
		return Proxy
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "wsl.localhost" || strings.HasSuffix(h, ".local") {
		return Direct
	}
	return Inspect
}

// sniffName 把客户端声明的名字（SNI 或 Host）换算成走法。空名字交给拨号目标。
func sniffName(name string) Route {
	if strings.TrimSpace(name) == "" {
		return ByDial
	}
	if r := ForHost(name); r != Inspect {
		return r
	}
	return Direct
}

// MaxInspect 是判断前最多缓存的客户端数据量。超过还没有主机名，就用拨号目标上的名字。
const MaxInspect = 16 << 10

// InspectFirst 从客户端第一段数据里取主机名。done 为假表示数据还不完整，等更多再看。
// TLS 取 ClientHello 的 SNI，明文 HTTP 取 Host（或绝对形式请求行里的主机）。
// 没有主机名时返回 ByDial：SSH、没有 SNI、没有 Host 都用拨号目标上的名字，不另作兜底。
func InspectFirst(b []byte) (route Route, name string, done bool) {
	if len(b) == 0 {
		return Proxy, "", false
	}
	if b[0] == 0x16 {
		sni, complete, ok := parseSNI(b)
		if !complete && len(b) < MaxInspect {
			return Proxy, "", false
		}
		if !ok {
			return ByDial, "", true
		}
		return sniffName(sni), sni, true
	}
	if maybeHTTP(b) {
		host, complete := httpHost(b)
		if !complete && len(b) < MaxInspect {
			return Proxy, "", false
		}
		return sniffName(host), host, true
	}
	return ByDial, "", true
}

var methods = []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "TRACE "}

// maybeHTTP：已经是一个 HTTP 方法开头，或者还太短、仍可能是。
func maybeHTTP(b []byte) bool {
	s := string(b)
	for _, m := range methods {
		if strings.HasPrefix(s, m) || (len(s) < len(m) && strings.HasPrefix(m, s)) {
			return true
		}
	}
	return false
}

// httpHost 从明文 HTTP 请求头里取主机名（去掉端口）。头没收全时 complete 为假。
func httpHost(b []byte) (host string, complete bool) {
	s := string(b)
	end := strings.Index(s, "\r\n\r\n")
	if end < 0 {
		return "", false
	}
	lines := strings.Split(s[:end], "\r\n")
	if parts := strings.Fields(lines[0]); len(parts) == 3 {
		target := strings.ToLower(parts[1])
		for _, scheme := range []string{"http://", "https://"} {
			if rest, ok := strings.CutPrefix(target, scheme); ok {
				if i := strings.IndexAny(rest, "/?#"); i >= 0 {
					rest = rest[:i]
				}
				return stripPort(rest), true
			}
		}
	} else {
		return "", true
	}
	for _, ln := range lines[1:] {
		name, value, ok := strings.Cut(ln, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "host") {
			return stripPort(strings.TrimSpace(value)), true
		}
	}
	return "", true
}

func stripPort(hostport string) string {
	if strings.HasPrefix(hostport, "[") {
		if i := strings.Index(hostport, "]"); i >= 0 {
			return hostport[1:i]
		}
		return hostport
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 && strings.Count(hostport, ":") == 1 {
		return hostport[:i]
	}
	return hostport
}

// parseSNI 从 TLS 记录里取 ClientHello 的 server_name。ClientHello 可以跨多条记录。
// complete 为假：数据还不够；ok 为假：不是 ClientHello 或没有 SNI。
func parseSNI(b []byte) (sni string, complete, ok bool) {
	var hs []byte
	rest := b
	need := -1
	for len(rest) >= 5 {
		if rest[0] != 0x16 {
			return "", true, false
		}
		n := int(rest[3])<<8 | int(rest[4])
		if len(rest) < 5+n {
			hs = append(hs, rest[5:]...)
			break
		}
		hs = append(hs, rest[5:5+n]...)
		rest = rest[5+n:]
		if need < 0 && len(hs) >= 4 {
			if hs[0] != 0x01 {
				return "", true, false
			}
			need = 4 + (int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3]))
		}
		if need >= 0 && len(hs) >= need {
			break
		}
	}
	if len(hs) >= 4 {
		if hs[0] != 0x01 {
			return "", true, false
		}
		need = 4 + (int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3]))
	}
	if need < 0 || len(hs) < need {
		return "", false, false
	}
	body := hs[4:need]
	p := 0
	skip := func(n int) bool {
		if p+n > len(body) {
			return false
		}
		p += n
		return true
	}
	u8 := func() (int, bool) {
		if p+1 > len(body) {
			return 0, false
		}
		v := int(body[p])
		p++
		return v, true
	}
	u16 := func() (int, bool) {
		if p+2 > len(body) {
			return 0, false
		}
		v := int(body[p])<<8 | int(body[p+1])
		p += 2
		return v, true
	}
	if !skip(2 + 32) {
		return "", true, false
	}
	if n, ok := u8(); !ok || !skip(n) {
		return "", true, false
	}
	if n, ok := u16(); !ok || !skip(n) {
		return "", true, false
	}
	if n, ok := u8(); !ok || !skip(n) {
		return "", true, false
	}
	extLen, ok := u16()
	if !ok || p+extLen > len(body) {
		return "", true, false
	}
	end := p + extLen
	for p+4 <= end {
		typ, _ := u16()
		n, _ := u16()
		if p+n > end {
			return "", true, false
		}
		if typ != 0 {
			p += n
			continue
		}
		data := body[p : p+n]
		if len(data) < 2 {
			return "", true, false
		}
		list := data[2:]
		for len(list) >= 3 {
			kind := list[0]
			l := int(list[1])<<8 | int(list[2])
			if len(list) < 3+l {
				return "", true, false
			}
			if kind == 0 {
				return string(list[3 : 3+l]), true, true
			}
			list = list[3+l:]
		}
		return "", true, false
	}
	return "", true, false
}
