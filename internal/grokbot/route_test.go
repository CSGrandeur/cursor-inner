package grokbot

import "testing"

func TestHTTPProxyURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "127.0.0.1:1080", want: "http://127.0.0.1:1080", ok: true},
		{in: "socks5://127.0.0.1:1080", want: "http://127.0.0.1:1080", ok: true},
		{in: "http://127.0.0.1:1080", want: "http://127.0.0.1:1080", ok: true},
		{in: "", ok: false},
		{in: "not a proxy", ok: false},
	}
	for _, tc := range cases {
		got, ok := HTTPProxyURL(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("%q -> %q %v, want %q %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRouted(t *testing.T) {
	proxy := "http://127.0.0.1:1080"
	cmd := `"Grok Bot.exe" --proxy-server=` + proxy + ` --disable-quic ` + EnvProxyMark
	if !Routed(cmd, proxy) {
		t.Fatal("expected routed")
	}
	if Routed(`"Grok Bot.exe" --proxy-server=`+proxy+` --disable-quic`, proxy) {
		t.Fatal("old launch without undici env-proxy mark is not routed")
	}
	if Routed(`"Grok Bot.exe"`, proxy) {
		t.Fatal("plain launch is not routed")
	}
	if Routed(cmd, "") {
		t.Fatal("empty proxy is not routed")
	}
}

func TestShouldRestartLeavesLoginReturnAlone(t *testing.T) {
	proxy := "http://127.0.0.1:1080"
	routed := `"Grok Bot.exe" --proxy-server=` + proxy + ` --disable-quic ` + EnvProxyMark
	returned := `"Grok Bot.exe" sand://login`
	if ShouldRestart([]string{routed, returned}, proxy) {
		t.Fatal("a second process without the proxy must not close the one that is already polling")
	}
	if !ShouldRestart([]string{returned}, proxy) {
		t.Fatal("a process without the proxy still needs a restart")
	}
	if ShouldRestart([]string{"", `"Grok Bot.exe" --type=gpu-process`}, proxy) {
		t.Fatal("empty or helper processes are not a restart")
	}
	if ShouldRestart([]string{`"Grok Bot.exe"`}, "") {
		t.Fatal("clearing the proxy does not restart a process we did not mark")
	}
	if !ShouldRestart([]string{routed}, "") {
		t.Fatal("clearing the proxy restarts a process that still has our arguments")
	}
}

func TestShouldRestartUpgradesOldProxyLaunch(t *testing.T) {
	proxy := "http://127.0.0.1:1080"
	old := `"Grok Bot.exe" --proxy-server=` + proxy + ` --disable-quic`
	if !ShouldRestart([]string{old}, proxy) {
		t.Fatal("process missing undici env-proxy mark must restart so NODE_USE_ENV_PROXY can apply")
	}
	upgraded := old + ` ` + EnvProxyMark
	if ShouldRestart([]string{upgraded, `"Grok Bot.exe" sand://login`}, proxy) {
		t.Fatal("upgraded process plus login return must not restart")
	}
}
