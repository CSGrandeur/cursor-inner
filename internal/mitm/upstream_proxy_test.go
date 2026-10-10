package mitm

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

func testCA(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// 未解密的主机（隧道转发）也必须经过配置的上游代理，而不是直连目标的 443。
func TestTunnelForSkippedHostGoesThroughConfiguredProxy(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	got := make(chan string, 1)
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- strings.TrimSpace(line)
		_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		time.Sleep(200 * time.Millisecond)
	}()

	cfg := config.Proxy{Enabled: true, Address: "http://" + upstream.Addr().String()}
	s := New(func() config.Proxy { return cfg }, func() []catalog.Entry { return nil }, func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	url, err := s.Start(testCA(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	c, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT example.invalid:443 HTTP/1.1\r\nHost: example.invalid:443\r\n\r\n")
	select {
	case line := <-got:
		if line != "CONNECT example.invalid:443 HTTP/1.1" {
			t.Fatalf("upstream proxy saw %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel never reached the configured proxy")
	}
}
