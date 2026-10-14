package dialer

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"cursor-inner/internal/config"
)

// fakeLookup 把主机名解析成固定地址，并记下被查过的名字。
func fakeLookup(t *testing.T, table map[string][]string) *[]string {
	t.Helper()
	var asked []string
	old := lookupIP
	lookupIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		asked = append(asked, host)
		ips, ok := table[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	t.Cleanup(func() { lookupIP = old })
	return &asked
}

// TestReservedDirectBothScopes：两种代理范围下，内网预留 IP、解析到内网的主机名直连；
// 官方域名即使解析到内网（DNS 重绑定）也走代理且根本不解析；公网/混合/解析失败照旧。
func TestReservedDirectBothScopes(t *testing.T) {
	asked := fakeLookup(t, map[string][]string{
		"intranet.example": {"10.1.2.3"},
		"cgnat.example":    {"100.64.1.2"},
		"mixed.example":    {"10.0.0.1", "8.8.8.8"},
		"public.example":   {"93.184.216.34"},
		"api.x.ai":         {"10.0.0.9"},
		"rebind.cursor.sh": {"127.0.0.1"},
		"fake.example":     {"198.18.0.5"},
	})
	for _, scope := range []string{"", config.ScopeOfficial} {
		r := &route{}
		via := r.dial("proxy")
		var d Func
		if scope == "" {
			d = reservedFirst(via, r.dial("direct"))
		} else {
			d = reservedFirst(split(via, r.dial("direct")), r.dial("direct"))
		}
		cases := []struct{ addr, want string }{
			{"10.1.2.3:443", "direct 10.1.2.3:443"},
			{"0.1.2.3:80", "direct 0.1.2.3:80"},
			{"100.100.1.1:443", "direct 100.100.1.1:443"},
			{"127.0.0.1:8080", "direct 127.0.0.1:8080"},
			{"192.0.0.8:80", "direct 192.0.0.8:80"},
			{"[fe80::1]:80", "direct [fe80::1]:80"},
			{"intranet.example:443", "direct 10.1.2.3:443"},
			{"cgnat.example:443", "direct 100.64.1.2:443"},
			{"api.x.ai:443", "proxy api.x.ai:443"},
			{"rebind.cursor.sh:443", "proxy rebind.cursor.sh:443"},
			{"198.18.0.5:443", "proxy 198.18.0.5:443"},
		}
		if scope == "" {
			cases = append(cases,
				struct{ addr, want string }{"mixed.example:443", "proxy mixed.example:443"},
				struct{ addr, want string }{"public.example:443", "proxy public.example:443"},
				struct{ addr, want string }{"fake.example:443", "proxy fake.example:443"},
				struct{ addr, want string }{"nx.example:443", "proxy nx.example:443"},
				struct{ addr, want string }{"8.8.8.8:53", "proxy 8.8.8.8:53"},
			)
		}
		for _, c := range cases {
			before := len(r.got())
			conn, err := d(context.Background(), "tcp", c.addr)
			if err != nil {
				t.Fatalf("scope %q %s: %v", scope, c.addr, err)
			}
			conn.Close()
			got := r.got()
			if len(got) != before+1 || got[before] != c.want {
				t.Fatalf("scope %q %s: calls %v, want %q", scope, c.addr, got[before:], c.want)
			}
		}
	}
	for _, h := range *asked {
		if h == "api.x.ai" || h == "rebind.cursor.sh" {
			t.Fatalf("official name %s must never be resolved locally", h)
		}
	}
}

// TestScopedGlobalReservedDirect：经 Scoped 构造的全局拨号，内网目标不进代理。
func TestScopedGlobalReservedDirect(t *testing.T) {
	addr, seen := connectProxy(t)
	fakeLookup(t, map[string][]string{"intranet.example": {"10.1.2.3"}})
	r := &route{}
	old := directDial
	directDial = func() Func { return r.dial("direct") }
	defer func() { directDial = old }()
	g, err := Scoped(config.Proxy{Enabled: true, Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"intranet.example:443", "api.x.ai:443"} {
		c, err := g(context.Background(), "tcp", a)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	if calls := r.got(); len(calls) != 1 || calls[0] != "direct 10.1.2.3:443" {
		t.Fatalf("direct calls %v", calls)
	}
	if got := seen(); len(got) != 1 || got[0] != "api.x.ai:443" {
		t.Fatalf("proxy saw %v", got)
	}
}
