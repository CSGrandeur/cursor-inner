package antigravitylaunch

import (
	"strings"
	"testing"
)

func TestEnvSetsHTTPSProxyForGoOAuthAndDropsElectronNode(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"ELECTRON_RUN_AS_NODE=1",
		"NO_PROXY=*",
		"HTTPS_PROXY=http://old:1",
	}
	got := Env("http://127.0.0.1:20378", base)
	m := map[string]string{}
	for _, entry := range got {
		name, value, _ := strings.Cut(entry, "=")
		m[name] = value
	}
	if _, ok := m["ELECTRON_RUN_AS_NODE"]; ok {
		t.Fatal("ELECTRON_RUN_AS_NODE must be stripped")
	}
	if m["PATH"] != "/bin" {
		t.Fatal(m["PATH"])
	}
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "CURSOR_INNER_LS_PROXY"} {
		if m[key] != "http://127.0.0.1:20378" {
			t.Fatalf("%s=%q", key, m[key])
		}
	}
	if strings.Contains(m["NO_PROXY"], "*") || !strings.Contains(m["NO_PROXY"], "127.0.0.1") {
		t.Fatal(m["NO_PROXY"])
	}
	if Args("") != nil {
		t.Fatal("empty proxy has no args")
	}
	joined := strings.Join(Args("http://127.0.0.1:20378"), " ")
	if !strings.Contains(joined, "--proxy-server=http://127.0.0.1:20378") || !strings.Contains(joined, "--disable-quic") || !strings.Contains(joined, "--use-env-proxy") {
		t.Fatal(joined)
	}
}
