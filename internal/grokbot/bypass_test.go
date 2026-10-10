package grokbot

import (
	"net"
	"strings"
	"testing"
)

func TestBypassIncludesPrivateAndLoopback(t *testing.T) {
	for _, c := range privateCIDRs {
		if !strings.Contains(LoopbackBypass, c) {
			t.Errorf("--proxy-bypass-list missing %s", c)
		}
		if !strings.Contains(LoopbackNoProxy, c) {
			t.Errorf("NO_PROXY missing %s", c)
		}
	}
	for _, h := range []string{"localhost", "127.0.0.1", "[::1]", "<local>", "*.local", "wsl.localhost"} {
		if !strings.Contains(LoopbackBypass, h) {
			t.Errorf("--proxy-bypass-list missing %s", h)
		}
	}
}

// 泄漏断言：官方域名与 TUN fake-ip 段绝不能出现在 bypass / NO_PROXY，也不能有全量通配符。
func TestBypassNeverLeaksOfficialOrFakeIP(t *testing.T) {
	for _, list := range []string{LoopbackBypass, LoopbackNoProxy} {
		low := strings.ToLower(list)
		for _, bad := range []string{"198.18", "198.19", "grok", "x.ai", "cursor", "cursorvm", "spacex", "<-loopback>"} {
			if strings.Contains(low, bad) {
				t.Errorf("%q must not appear in %q", bad, list)
			}
		}
	}
	for _, tok := range strings.Split(LoopbackBypass, ";") {
		switch tok {
		case "*", "*://*", "0.0.0.0/0", "::/0":
			t.Errorf("catch-all wildcard token in bypass: %q", tok)
		}
	}
}

// 语义断言：LAN/WSL 落入 bypass 网段；fake-ip 与公网官方 IP 绝不落入。
func TestBypassRangeSemantics(t *testing.T) {
	var nets []*net.IPNet
	for _, c := range privateCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("bad cidr %s: %v", c, err)
		}
		nets = append(nets, n)
	}
	inAny := func(ip string) bool {
		p := net.ParseIP(ip)
		for _, n := range nets {
			if n.Contains(p) {
				return true
			}
		}
		return false
	}
	for _, ip := range []string{"172.20.0.1", "192.168.1.5", "10.1.2.3", "169.254.0.1", "fe80::1"} {
		if !inAny(ip) {
			t.Errorf("%s (LAN/WSL) should be bypassed", ip)
		}
	}
	for _, ip := range []string{"198.18.0.1", "198.19.255.254", "104.18.19.125"} {
		if inAny(ip) {
			t.Errorf("%s must NOT be in a bypass range (official/fake-ip would leak)", ip)
		}
	}
}
