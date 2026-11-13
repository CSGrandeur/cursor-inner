package antigravitylaunch

import (
	"strings"
	"testing"
)

func TestEnvSetsHTTPSProxyForGoOAuthAndDropsElectronNode(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"ELECTRON_RUN_AS_NODE=1",
		"CURSOR_INNER_LS_PROXY=http://old:2",
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
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"} {
		if m[key] != "http://127.0.0.1:20378" {
			t.Fatalf("%s=%q", key, m[key])
		}
	}
	if _, ok := m["CURSOR_INNER_LS_PROXY"]; ok {
		t.Fatal("CURSOR_INNER_LS_PROXY must not be injected")
	}
	wslEnv, ok := m["WSLENV"]
	if !ok || wslEnv == "" {
		t.Fatal("WSLENV must be set for scoped process inheritance")
	}
	if strings.Contains(wslEnv, "/p") || strings.Contains(wslEnv, "/l") || strings.Contains(wslEnv, "CURSOR_INNER_LS_PROXY") {
		t.Fatalf("unexpected flags or legacy var in WSLENV: %q", wslEnv)
	}
	parts := strings.Split(wslEnv, ":")
	have := map[string]bool{}
	for _, p := range parts {
		have[p] = true
	}
	for _, want := range []string{
		"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY",
		"GRPC_PROXY", "NODE_USE_ENV_PROXY",
	} {
		if !have[want] {
			t.Fatalf("WSLENV %q missing %q", wslEnv, want)
		}
	}
	for _, lowerKey := range []string{"https_proxy", "http_proxy", "all_proxy", "no_proxy", "grpc_proxy"} {
		if have[lowerKey] {
			t.Fatalf("WSLENV %q must not contain lowercase %q (produces empty shadow var on Windows)", wslEnv, lowerKey)
		}
	}
	if len(parts) != 6 {
		t.Fatalf("expected 6 canonical uppercase tokens in WSLENV, got %v", parts)
	}
	// 大写环境变量必须排在小写后面，保证 Windows PEB 去重后保留大写键供 WSLENV 透传。
	idxUpper, idxLower := -1, -1
	noUpper, noLower := -1, -1
	for i, entry := range got {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "HTTPS_PROXY":
			idxUpper = i
		case "https_proxy":
			idxLower = i
		case "NO_PROXY":
			noUpper = i
		case "no_proxy":
			noLower = i
		}
	}
	if idxLower < 0 || idxUpper <= idxLower || noLower < 0 || noUpper <= noLower {
		t.Fatalf("expected uppercase proxy keys after lowercase in Env: HTTPS_PROXY@%d https_proxy@%d NO_PROXY@%d no_proxy@%d", idxUpper, idxLower, noUpper, noLower)
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
	if strings.Contains(joined, "--proxy_server_url") {
		t.Fatalf("Args must not contain --proxy_server_url: %q", joined)
	}
}

func TestEnvManagesScopedWSLENVCleanly(t *testing.T) {
	// 1. 有代理且原 WSLENV 含自定义项、旧 /p 标记和历史 CURSOR_INNER_LS_PROXY：保留自定义项，清洗旧代理并追加无 /p 的受管列表。
	got := Env("http://127.0.0.1:20378", []string{
		"PATH=/bin",
		"CURSOR_INNER_LS_PROXY=http://old:1",
		"WSLENV=FOO/p:HTTPS_PROXY/p:https_proxy:CURSOR_INNER_LS_PROXY",
	})
	var wslVals []string
	for _, entry := range got {
		name, val, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "WSLENV") {
			wslVals = append(wslVals, val)
		}
		if strings.Contains(entry, "--proxy_server_url") || strings.EqualFold(name, "CURSOR_INNER_LS_PROXY") {
			t.Fatalf("injected forbidden entry %q", entry)
		}
	}
	if len(wslVals) != 1 {
		t.Fatalf("expected 1 WSLENV entry, got %v", wslVals)
	}
	tokens := strings.Split(wslVals[0], ":")
	if tokens[0] != "FOO/p" {
		t.Fatalf("expected FOO/p preserved first, got %q", wslVals[0])
	}
	if len(tokens[1:]) != 6 {
		t.Fatalf("expected 6 canonical uppercase tokens after FOO/p, got %v", tokens[1:])
	}
	for _, tok := range tokens[1:] {
		if strings.Contains(tok, "/p") || strings.Contains(tok, "/l") || strings.EqualFold(tok, "CURSOR_INNER_LS_PROXY") || tok != strings.ToUpper(tok) {
			t.Fatalf("dirty token in merged WSLENV: %q (full=%q)", tok, wslVals[0])
		}
	}

	// 2. 空代理且原 WSLENV 含自定义项与受管代理项：只留下 FOO/p。
	clearedKeepCustom := Env("", []string{"PATH=/bin", "WSLENV=FOO/p:HTTPS_PROXY:no_proxy"})
	wslVals = nil
	for _, entry := range clearedKeepCustom {
		name, val, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "WSLENV") {
			wslVals = append(wslVals, val)
		}
	}
	if len(wslVals) != 1 || wslVals[0] != "FOO/p" {
		t.Fatalf("expected WSLENV=FOO/p, got %v", wslVals)
	}

	// 3. 空代理且原 WSLENV 只有受管代理项（或无 WSLENV）：完全不写 WSLENV。
	for _, base := range [][]string{
		{"PATH=/bin"},
		{"PATH=/bin", "WSLENV=HTTPS_PROXY:HTTP_PROXY"},
	} {
		clearedAll := Env("", base)
		for _, entry := range clearedAll {
			name, _, _ := strings.Cut(entry, "=")
			if strings.EqualFold(name, "WSLENV") || strings.EqualFold(name, "CURSOR_INNER_LS_PROXY") {
				t.Fatalf("expected no WSLENV/CURSOR_INNER_LS_PROXY in %v, found %q", base, entry)
			}
		}
	}
}
