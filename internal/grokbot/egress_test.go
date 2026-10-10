package grokbot

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestProxyEnvForcesCONNECTThroughProxy 证明：即使进程环境里有 NO_PROXY=*（PAC/系统
// 代理常见残留），按 ProxyEnv 装配后的客户端仍对目标发 CONNECT，而不会直连原点。
// 这对应 v2rayN PAC 模式下「部分流量绕过 cursor-inner 注入的代理」的回归。
func TestProxyEnvForcesCONNECTThroughProxy(t *testing.T) {
	originHost, originPort := startTLSOrigin(t)
	proxyURL, sawCONNECT := startCONNECTProxy(t)

	env := ProxyEnv(proxyURL, []string{"NO_PROXY=*", "no_proxy=*"})
	m := envMap(env)
	if m["NO_PROXY"] == "*" || strings.Contains(m["NO_PROXY"], "*") {
		t.Fatalf("ProxyEnv must not keep NO_PROXY=*: %q", m["NO_PROXY"])
	}

	proxyParsed, err := url.Parse(m["HTTPS_PROXY"])
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyParsed),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         "origin.test",
			},
		},
	}
	target := "https://" + net.JoinHostPort(originHost, originPort) + "/ping"
	res, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	wantHost := net.JoinHostPort(originHost, originPort)
	if !sawCONNECT(wantHost) {
		t.Fatalf("proxy never saw CONNECT %s — egress leaked past the proxy", wantHost)
	}
}

// TestDirectWouldBypassWhenNoProxyStar 对照：若错误地保留 NO_PROXY=*，同一客户端会直连、
// 代理收不到 CONNECT。用来说明「继承系统 no_proxy」为何在 PAC 下会漏。
func TestDirectWouldBypassWhenNoProxyStar(t *testing.T) {
	originHost, originPort := startTLSOrigin(t)
	_, sawCONNECT := startCONNECTProxy(t)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return nil, nil
			},
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         "origin.test",
			},
		},
	}
	target := "https://" + net.JoinHostPort(originHost, originPort) + "/ping"
	res, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	wantHost := net.JoinHostPort(originHost, originPort)
	if sawCONNECT(wantHost) {
		t.Fatal("control case expected direct dial, but proxy saw CONNECT")
	}
}

func startTLSOrigin(t *testing.T) (host, port string) {
	t.Helper()
	cert := mustEgressCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				io.Copy(io.Discard, req.Body)
				req.Body.Close()
				resp := &http.Response{
					StatusCode:    200,
					ProtoMajor:    1,
					ProtoMinor:    1,
					Header:        make(http.Header),
					Body:          io.NopCloser(strings.NewReader("ok")),
					ContentLength: 2,
				}
				resp.Write(c)
			}(c)
		}
	}()
	host, port, err = net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func startCONNECTProxy(t *testing.T) (proxyURL string, saw func(hostport string) bool) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if req.Method != http.MethodConnect {
					return
				}
				mu.Lock()
				seen[req.Host] = true
				mu.Unlock()
				dest, err := net.DialTimeout("tcp", req.Host, 2*time.Second)
				if err != nil {
					c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				defer dest.Close()
				c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				errc := make(chan struct{}, 2)
				go func() { io.Copy(dest, br); errc <- struct{}{} }()
				go func() { io.Copy(c, dest); errc <- struct{}{} }()
				<-errc
			}(c)
		}
	}()
	proxyURL = "http://" + ln.Addr().String()
	saw = func(hostport string) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := seen[hostport]
			mu.Unlock()
			if ok {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return seen[hostport]
	}
	return proxyURL, saw
}

func mustEgressCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "origin.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"origin.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
