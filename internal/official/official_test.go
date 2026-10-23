package official

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

func TestForHost(t *testing.T) {
	cases := map[string]Route{
		"grok.com":                 Proxy,
		"API.X.AI.":                Proxy,
		"us3.cursorvm.com":         Proxy,
		"api2.cursor.sh":           Proxy,
		"cloudflare-dns.com":       Proxy,
		"dns.google":               Proxy,
		"198.18.0.7":               Proxy,
		"198.19.255.1":             Proxy,
		"[fc00::5]":                Proxy,
		"203.0.113.9":              Proxy,
		"2001:db8::1":              Proxy,
		"":                         Proxy,
		"127.0.0.1":                Direct,
		"::1":                      Direct,
		"192.168.1.20":             Direct,
		"172.20.3.4":               Direct,
		"10.1.2.3":                 Direct,
		"fd12::1":                  Direct,
		"::ffff:192.168.1.1":       Direct,
		"localhost":                Direct,
		"wsl.localhost":            Direct,
		"printer.local":            Direct,
		"book.example.com":         Inspect,
		"notgrok.com":              Inspect,
		"x.ai.example.com":         Inspect,
		"github.com":               Inspect,
		"api.openai.com":           Proxy,
		"ab.chatgpt.com":           Proxy,
		"cdn.oaistatic.com":        Proxy,
		"files.oaiusercontent.com": Proxy,
		"auth.oaistatsig.com":      Proxy,
		"api.stripe.com":           Inspect,
	}
	for host, want := range cases {
		if got := ForHost(host); got != want {
			t.Errorf("ForHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// clientHello 让 crypto/tls 真的写一个 ClientHello，原样截下来。
func clientHello(t *testing.T, sni string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		c := tls.Client(a, &tls.Config{ServerName: sni, InsecureSkipVerify: sni == "", NextProtos: []string{"h2", "http/1.1"}})
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_ = c.Handshake()
	}()
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := b.Read(buf)
		out = append(out, buf[:n]...)
		if len(out) >= 5 && len(out) >= 5+int(out[3])<<8|int(out[4]) {
			return out
		}
		if err != nil {
			t.Fatalf("read hello: %v", err)
		}
	}
}

func TestInspectTLS(t *testing.T) {
	for sni, want := range map[string]Route{
		"book.example.com": Direct,
		"api.x.ai":         Proxy,
		"grok.com":         Proxy,
		"api.openai.com":   Proxy,
		"localhost":        Direct,
	} {
		hello := clientHello(t, sni)
		got, name, done := InspectFirst(hello)
		if !done || got != want || name != sni {
			t.Errorf("SNI %q: got %v %q done=%v, want %v", sni, got, name, done, want)
		}
		// 一个字节一个字节到，收全之前都要等。
		for i := 1; i < len(hello); i++ {
			if _, _, done := InspectFirst(hello[:i]); done {
				t.Fatalf("SNI %q: decided after %d/%d bytes", sni, i, len(hello))
			}
		}
	}
	// 没有 SNI：这段数据里没有主机名，交给拨号目标。
	if got, _, done := InspectFirst(clientHello(t, "")); !done || got != ByDial {
		t.Fatalf("no SNI: got %v done=%v", got, done)
	}
}

func TestInspectSplitRecords(t *testing.T) {
	hello := clientHello(t, "book.example.com")
	frag := hello[5:]
	// 把同一个 ClientHello 拆成两条记录。
	half := len(frag) / 2
	rec := func(p []byte) []byte {
		return append([]byte{0x16, 0x03, 0x01, byte(len(p) >> 8), byte(len(p))}, p...)
	}
	split := append(rec(frag[:half]), rec(frag[half:])...)
	if _, _, done := InspectFirst(split[:len(split)-1]); done {
		t.Fatal("decided before the second record arrived")
	}
	if got, name, done := InspectFirst(split); !done || got != Direct || name != "book.example.com" {
		t.Fatalf("split: %v %q %v", got, name, done)
	}
}

func TestInspectHTTPAndOther(t *testing.T) {
	cases := []struct {
		in   string
		want Route
		done bool
	}{
		{"GET / HTTP/1.1\r\nHost: book.example.com\r\n\r\n", Direct, true},
		{"GET / HTTP/1.1\r\nHost: api.x.ai:80\r\n\r\n", Proxy, true},
		{"GET http://grok.com/x HTTP/1.1\r\nHost: book.example.com\r\n\r\n", Proxy, true},
		{"GET / HTTP/1.1\r\n\r\n", ByDial, true},
		{"GET / HTTP/1.1\r\nHost: book.example.com\r\n", Proxy, false},
		{"GE", Proxy, false},
		{"SSH-2.0-OpenSSH\r\n", ByDial, true},
		{"\x05\x01\x00", ByDial, true},
	}
	for _, c := range cases {
		got, _, done := InspectFirst([]byte(c.in))
		if done != c.done || (done && got != c.want) {
			t.Errorf("%q: got %v done=%v, want %v done=%v", c.in, got, done, c.want, c.done)
		}
	}
	big := make([]byte, MaxInspect)
	copy(big, "GET / HTTP/1.1\r\nX: ")
	if got, _, done := InspectFirst(big); !done || got != ByDial {
		t.Fatalf("oversized header: %v %v", got, done)
	}
}

// Cursor 3.26.13 的 product.json 新增独立后端 api.cursor.spacex.com / api.cursor-staging.spacex.com，
// 必须走代理；但 spacex.com 其它子域不是 Cursor 的流量。
func TestCursorSpacexBackendsMustProxy(t *testing.T) {
	for _, h := range []string{"api.cursor.spacex.com", "cursor.spacex.com", "api.cursor-staging.spacex.com"} {
		if !MustProxy(h) {
			t.Errorf("%s should be official", h)
		}
	}
	for _, h := range []string{"spacex.com", "www.spacex.com", "starlink.spacex.com"} {
		if MustProxy(h) {
			t.Errorf("%s should not be official", h)
		}
	}
}

// 精确主机名：只收这几个，同一云上的兄弟主机（别的 S3 桶、别的 Blob 账户）不算官方。
func TestExactOfficialHostsDoNotBroaden(t *testing.T) {
	for _, h := range []string{"agent-stores.s3.us-east-1.amazonaws.com", "AGENT-STORES.s3.us-east-1.amazonaws.com.", "workoscdn.com", "cursor.blob.core.windows.net"} {
		if !MustProxy(h) || !Name(h) {
			t.Errorf("%s should be official", h)
		}
	}
	for _, h := range []string{"other-bucket.s3.us-east-1.amazonaws.com", "s3.us-east-1.amazonaws.com", "amazonaws.com", "x.agent-stores.s3.us-east-1.amazonaws.com", "evil.blob.core.windows.net", "blob.core.windows.net", "cdn.workoscdn.com", "workos.com"} {
		if MustProxy(h) || Name(h) {
			t.Errorf("%s should not be official", h)
		}
	}
}

// 每个官方名字（后缀名单、精确主机名单、DoH）在“仅官方”的每条判断上都必须走代理：
// 拨号目标（ForHost）、首包里的 SNI/Host（sniffName）、MustProxy。
// 回归：2026-10-21 精确主机 agent-stores S3 在 Grok Bot 桥上按首包被判成直连。
func TestEveryOfficialNameProxiesOnEveryPath(t *testing.T) {
	names := append(append(append([]string(nil), Suffixes...), Hosts...), alsoProxy...)
	for _, n := range names {
		for _, h := range []string{n, strings.ToUpper(n) + "."} {
			if r := ForHost(h); r != Proxy {
				t.Errorf("ForHost(%q) = %v, want proxy", h, r)
			}
			if r := sniffName(h); r != Proxy {
				t.Errorf("sniffName(%q) = %v, want proxy", h, r)
			}
			if !MustProxy(h) {
				t.Errorf("MustProxy(%q) = false", h)
			}
		}
	}
	for _, h := range Hosts {
		if r := ForHost("x." + h); r == Proxy {
			t.Errorf("ForHost(sub of exact %q) = proxy, exact hosts must not broaden", h)
		}
	}
}
