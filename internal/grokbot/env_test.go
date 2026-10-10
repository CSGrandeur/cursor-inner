package grokbot

import (
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
