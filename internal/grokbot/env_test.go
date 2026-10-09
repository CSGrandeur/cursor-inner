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
	want := []string{"--proxy-server=http://127.0.0.1:1080", "--disable-quic", EnvProxyMark}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
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
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if m[key] != "http://127.0.0.1:1080" {
			t.Fatalf("%s=%q", key, m[key])
		}
	}
	if m["NO_PROXY"] != "localhost,127.0.0.1" || m["no_proxy"] != "localhost,127.0.0.1" {
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
	}
	got := ProxyEnv("", base)
	m := envMap(got)
	if m["PATH"] != "/bin" {
		t.Fatal(m)
	}
	for _, key := range []string{"HTTPS_PROXY", "NODE_USE_ENV_PROXY", "ELECTRON_RUN_AS_NODE", "HTTP_PROXY", "NO_PROXY"} {
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

func envMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, val, _ := strings.Cut(entry, "=")
		out[name] = val
	}
	return out
}
