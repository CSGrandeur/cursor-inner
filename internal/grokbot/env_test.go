package grokbot

import (
	"net"
	"strings"
	"testing"
)

func TestProxyArgs(t *testing.T) {
	if ProxyArgs("") != nil {
		t.Fatal("empty proxy has no args")
	}
	got := ProxyArgs("http://127.0.0.1:1080")
	want := []string{
		"--proxy-server=http://127.0.0.1:1080",
		"--disable-quic",
		EnvProxyMark,
		"--proxy-bypass-list=" + LoopbackBypass,
		WebRTCPolicy,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	joined := strings.Join(got, " ")
	for _, bad := range []string{"--no-proxy-server", "--proxy-auto-detect", "--proxy-pac-url", "<-loopback>", "direct://"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("must not contain %q: %s", bad, joined)
		}
	}
}

func TestProxyEnvSetsUndiciProxy(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HTTP_PROXY=http://old:1",
		"https_proxy=http://old:1",
		"NODE_USE_ENV_PROXY=0",
		"ELECTRON_RUN_AS_NODE=1",
		"ALL_PROXY=http://old:1",
		"NO_PROXY=*",
	}
	got := ProxyEnv("http://127.0.0.1:1080", base)
	m := envMap(got)
	if m["PATH"] != "/bin" {
		t.Fatal("kept unrelated vars")
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "GRPC_PROXY", "grpc_proxy"} {
		if m[key] != "http://127.0.0.1:1080" {
			t.Fatalf("%s=%q", key, m[key])
		}
	}
	if !strings.HasPrefix(m["NO_PROXY"], LoopbackNoProxy) || !strings.HasPrefix(m["no_proxy"], LoopbackNoProxy) {
		t.Fatal(m["NO_PROXY"], m["no_proxy"])
	}
	if m["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatal("undici needs NODE_USE_ENV_PROXY=1, got", m["NODE_USE_ENV_PROXY"])
	}
	if _, ok := m["ELECTRON_RUN_AS_NODE"]; ok {
		t.Fatal("ELECTRON_RUN_AS_NODE must be removed")
	}
}

func TestProxyEnvClearsProxyVars(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HTTPS_PROXY=http://127.0.0.1:1080",
		"NODE_USE_ENV_PROXY=1",
		"ELECTRON_RUN_AS_NODE=1",
		"GRPC_PROXY=http://127.0.0.1:1080",
	}
	got := ProxyEnv("", base)
	m := envMap(got)
	if m["PATH"] != "/bin" {
		t.Fatal(m)
	}
	for _, key := range []string{"HTTPS_PROXY", "NODE_USE_ENV_PROXY", "ELECTRON_RUN_AS_NODE", "HTTP_PROXY", "NO_PROXY", "GRPC_PROXY"} {
		if _, ok := m[key]; ok {
			t.Fatalf("cleared env still has %s", key)
		}
	}
	for _, entry := range got {
		if strings.HasPrefix(strings.ToUpper(entry), "NODE_USE_ENV_PROXY=") {
			t.Fatal(entry)
		}
	}
}

func TestProxyEnvIgnoresInheritedNoProxy(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"NO_PROXY=*,cdn.example.com,.local",
		"no_proxy=api.x.ai,grok.com",
	}
	env := ProxyEnv("http://127.0.0.1:1080", base)
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		got, n := envValue(env, key)
		if n != 1 || !strings.HasPrefix(got, LoopbackNoProxy) {
			t.Fatalf("%s = %q (x%d)\nwant prefix %q", key, got, n, LoopbackNoProxy)
		}
		if strings.Contains(got, "*") || strings.Contains(got, "cdn.example.com") || strings.Contains(got, "api.x.ai") || strings.Contains(got, "grok.com") {
			t.Fatalf("inherited no_proxy leaked: %s", got)
		}
	}
}

func envMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, val, _ := strings.Cut(entry, "=")
		out[name] = val
	}
	return out
}

func TestProxyEnvUppercaseFollowsLowercaseForWindowsPEB(t *testing.T) {
	got := ProxyEnv("http://127.0.0.1:1080", nil)
	pos := make(map[string]int, len(got))
	for i, entry := range got {
		k, _, _ := strings.Cut(entry, "=")
		pos[k] = i
	}
	pairs := [][2]string{
		{"https_proxy", "HTTPS_PROXY"},
		{"http_proxy", "HTTP_PROXY"},
		{"all_proxy", "ALL_PROXY"},
		{"grpc_proxy", "GRPC_PROXY"},
		{"no_proxy", "NO_PROXY"},
	}
	for _, p := range pairs {
		li, lok := pos[p[0]]
		ui, uok := pos[p[1]]
		if !lok || !uok || ui <= li {
			t.Fatalf("expected %s (%d) after %s (%d)", p[1], ui, p[0], li)
		}
	}
}

func envValue(env []string, key string) (string, int) {
	val, n := "", 0
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if k == key {
			val, n = v, n+1
		}
	}
	return val, n
}

func mustCIDRAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	ipnet.IP = ip
	return ipnet
}

func TestLoopbackBypassAndNoProxyCoverLocalHostsAndIPv6(t *testing.T) {
	bypassTokens := map[string]bool{}
	for _, tok := range strings.Split(LoopbackBypass, ";") {
		bypassTokens[tok] = true
	}
	for _, want := range []string{
		"localhost", "*.localhost", "127.0.0.1", "[::1]", "<local>", "*.local", "wsl.localhost",
		"host.docker.internal", "gateway.docker.internal", "kubernetes.docker.internal",
	} {
		if !bypassTokens[want] {
			t.Fatalf("LoopbackBypass missing %q: %s", want, LoopbackBypass)
		}
	}
	if bypassTokens["*"] || strings.Contains(LoopbackBypass, "198.18.") {
		t.Fatalf("LoopbackBypass must not contain wildcard or fake-ip: %s", LoopbackBypass)
	}

	noProxyTokens := map[string]bool{}
	for _, tok := range strings.Split(LoopbackNoProxy, ",") {
		noProxyTokens[tok] = true
	}
	for _, want := range []string{
		"localhost", ".localhost", "127.0.0.1", "::1", "[::1]", ".local", "wsl.localhost",
		"host.docker.internal", "gateway.docker.internal", "kubernetes.docker.internal",
	} {
		if !noProxyTokens[want] {
			t.Fatalf("LoopbackNoProxy missing %q: %s", want, LoopbackNoProxy)
		}
	}
}

func TestProxyEnvInjectsReservedInterfaceLiteralsWithoutMutatingProxyArgs(t *testing.T) {
	old := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = old })

	argsBefore := ProxyArgs("http://127.0.0.1:1080")
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			mustCIDRAddr(t, "127.0.0.1/8"),
			mustCIDRAddr(t, "::1/128"),
			mustCIDRAddr(t, "172.20.10.5/24"),
			mustCIDRAddr(t, "192.168.50.8/24"),
			mustCIDRAddr(t, "100.88.1.2/32"),
			mustCIDRAddr(t, "172.17.0.1/16"),
			mustCIDRAddr(t, "fd12:3456::9/64"),
			mustCIDRAddr(t, "fe80::1234/64"),
			mustCIDRAddr(t, "198.18.0.1/15"),
			mustCIDRAddr(t, "fc00::1/18"),
			mustCIDRAddr(t, "192.0.2.1/30"),
			mustCIDRAddr(t, "fdfe:dcba:9876::1/126"),
			mustCIDRAddr(t, "8.8.8.8/32"),
			mustCIDRAddr(t, "2001:4860:4860::8888/128"),
		}, nil
	}
	argsAfter := ProxyArgs("http://127.0.0.1:1080")
	if strings.Join(argsBefore, "\x00") != strings.Join(argsAfter, "\x00") {
		t.Fatalf("ProxyArgs must stay static across interface changes: %v vs %v", argsBefore, argsAfter)
	}

	env := ProxyEnv("http://127.0.0.1:1080", nil)
	noProxy, _ := envValue(env, "NO_PROXY")
	counts := map[string]int{}
	for _, tok := range strings.Split(noProxy, ",") {
		counts[tok]++
	}
	for _, want := range []string{
		"172.20.10.5", "192.168.50.8", "100.88.1.2",
		"10.255.255.254", "172.17.0.1",
		"fd12:3456::9", "[fd12:3456::9]",
	} {
		if counts[want] != 1 {
			t.Fatalf("expected %q once in NO_PROXY, got count=%d (%s)", want, counts[want], noProxy)
		}
	}
	for _, forbidden := range []string{
		"fe80::1234", "[fe80::1234]",
		"198.18.0.1", "fc00::1", "[fc00::1]",
		"192.0.2.1", "fdfe:dcba:9876::1", "[fdfe:dcba:9876::1]",
		"8.8.8.8", "2001:4860:4860::8888", "[2001:4860:4860::8888]",
	} {
		if counts[forbidden] != 0 {
			t.Fatalf("forbidden address %q leaked into NO_PROXY: %s", forbidden, noProxy)
		}
	}
}
