package procfwd

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

// TestVirtualHostsForward 在独立的网络和 hosts 挂载里跑。
// 普通 go test 会跳过，避免改本机的 /etc/hosts。
func TestVirtualHostsForward(t *testing.T) {
	if os.Getenv("PROCFWD_NS") != "1" {
		t.Skip("需要 PROCFWD_NS=1，并且跑在独立的网络和 hosts 挂载里")
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	socksLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socksLn.Close()
	got := make(chan socksHit, 4)
	go serveSocks(socksLn, got)

	dial, err := dialer.FromProxy(config.Proxy{Enabled: true, Address: socksLn.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	f := New(dial)
	f.flush = false
	if err := f.Sync(true); err != nil {
		t.Fatal(err)
	}

	hosts, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		ips, err := net.LookupHost(name)
		if err != nil || len(ips) != 1 || ips[0] != "127.0.0.1" {
			t.Fatalf("%s resolved %v %v", name, ips, err)
		}
		if !bytes.Contains(hosts, []byte("127.0.0.1 "+name+"\n")) {
			t.Fatalf("hosts missing %s\n%s", name, hosts)
		}
	}
	if bytes.Contains(hosts, []byte("api2.cursor.sh")) || bytes.Contains(hosts, []byte("api5.cursor.sh")) {
		t.Fatalf("decrypt names were redirected\n%s", hosts)
	}
	if !bytes.Contains(hosts, before) && !bytes.Contains(hosts, bytes.TrimRight(before, "\n")) {
		t.Fatalf("original hosts dropped\n%s", hosts)
	}

	conn, err := net.DialTimeout("tcp", "api3.cursor.sh:443", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	hello := helloFor(t, "api3.cursor.sh")
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}
	hit := recvHit(t, got)
	if hit.atyp != 3 || hit.host != "api3.cursor.sh" || hit.port != 443 {
		t.Fatalf("proxy saw %+v", hit)
	}
	if !bytes.Equal(hit.payload, hello) {
		t.Fatalf("payload changed: got %d bytes, sent %d", len(hit.payload), len(hello))
	}
	_ = conn.Close()

	other, err := net.DialTimeout("tcp", "127.0.0.1:443", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Write(helloFor(t, "example.com")); err != nil {
		t.Fatal(err)
	}
	select {
	case hit := <-got:
		t.Fatalf("proxied a name outside the list: %+v", hit)
	case <-time.After(300 * time.Millisecond):
	}
	_ = other.Close()

	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("hosts not restored\nbefore %q\nafter %q", before, after)
	}
	// 本进程里的解析会把 hosts 缓存几秒。另起一次查询，确认系统已经看不到转发。
	out, err := exec.Command("getent", "hosts", "api3.cursor.sh").CombinedOutput()
	if err == nil && bytes.Contains(out, []byte("127.0.0.1")) {
		t.Fatalf("api3.cursor.sh still resolves to 127.0.0.1: %s", out)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:443", 300*time.Millisecond); err == nil {
		t.Fatal("443 still open after restore")
	}
}

type socksHit struct {
	atyp    byte
	host    string
	port    int
	payload []byte
}

func recvHit(t *testing.T, got <-chan socksHit) socksHit {
	t.Helper()
	select {
	case hit := <-got:
		return hit
	case <-time.After(2 * time.Second):
		t.Fatal("proxy was not dialed")
	}
	return socksHit{}
}

func readRecord(conn net.Conn) []byte {
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var buf bytes.Buffer
	tmp := make([]byte, 2048)
	for buf.Len() < 5 {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			return buf.Bytes()
		}
	}
	need := 5 + int(buf.Bytes()[3])<<8 | int(buf.Bytes()[4])
	for buf.Len() < need {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.Bytes()
}

func serveSocks(ln net.Listener, got chan<- socksHit) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			hit, err := readSocks(conn)
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
			hit.payload = readRecord(conn)
			got <- hit
		}(conn)
	}
}

func readSocks(conn net.Conn) (socksHit, error) {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return socksHit{}, err
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return socksHit{}, err
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return socksHit{}, err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return socksHit{}, err
	}
	hit := socksHit{atyp: req[3]}
	switch req[3] {
	case 1:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return socksHit{}, err
		}
		hit.host = net.IP(addr).String()
	case 3:
		n := make([]byte, 1)
		if _, err := io.ReadFull(conn, n); err != nil {
			return socksHit{}, err
		}
		name := make([]byte, int(n[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return socksHit{}, err
		}
		hit.host = string(name)
	default:
		return socksHit{}, io.ErrUnexpectedEOF
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return socksHit{}, err
	}
	hit.port = int(port[0])<<8 | int(port[1])
	return hit, nil
}
