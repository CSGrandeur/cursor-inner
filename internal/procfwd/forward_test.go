package procfwd

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerNameFromHello(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: "API3.cursor.sh", InsecureSkipVerify: true})
		_ = c.Handshake()
		_ = c.Close()
	}()
	name, raw, err := readHello(server)
	if err != nil {
		t.Fatal(err)
	}
	if name != "api3.cursor.sh" {
		t.Fatalf("name %q", name)
	}
	again, err := serverName(raw)
	if err != nil || again != name {
		t.Fatalf("serverName %q %v", again, err)
	}
}

func TestTapRecordsChildBytes(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		rest := make([]byte, int(hdr[3])<<8|int(hdr[4]))
		_, _ = io.ReadFull(c, rest)
		_, _ = c.Write([]byte("pong"))
	}()
	var toNet, fromNet bytes.Buffer
	done := make(chan struct{}, 2)
	SetTap(Tap{Open: func(host, via string) (io.WriteCloser, io.WriteCloser) {
		if via != "child" || host != "api3.cursor.sh:443" {
			t.Errorf("tap %s %s", via, host)
		}
		return &signalClose{Buffer: &toNet, done: done}, &signalClose{Buffer: &fromNet, done: done}
	}})
	t.Cleanup(func() { SetTap(Tap{}) })

	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := New(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", up.Addr().String())
	})
	f.port = "0"
	f.path = path
	f.flush = false
	if err := f.Sync(true); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	c, err := tls.Dial("tcp", f.ln.Addr().String(), &tls.Config{ServerName: "api3.cursor.sh", InsecureSkipVerify: true})
	if err == nil {
		_ = c.Close()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("tap did not close")
		}
	}
	if fromNet.String() != "pong" || !bytes.Contains(toNet.Bytes(), []byte("api3.cursor.sh")) {
		t.Fatalf("up %d bytes down %q", toNet.Len(), fromNet.String())
	}
}

type signalClose struct {
	*bytes.Buffer
	done chan struct{}
}

func (s signalClose) Close() error {
	s.done <- struct{}{}
	return nil
}

func TestSpliceDialsName(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	got := make(chan string, 1)
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write([]byte("ok"))
	}()

	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := New(func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := net.Dial("tcp", up.Addr().String())
		got <- addr
		return c, err
	})
	f.port = "0"
	f.path = path
	f.flush = false
	if err := f.Sync(true); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	go func() {
		c, err := tls.Dial("tcp", f.ln.Addr().String(), &tls.Config{ServerName: "api3.cursor.sh", InsecureSkipVerify: true})
		if err == nil {
			_ = c.Close()
		}
	}()
	select {
	case addr := <-got:
		if addr != "api3.cursor.sh:443" {
			t.Fatalf("dialed %s", addr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial was not called")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hostsHeld(path) || ApplyBlock("127.0.0.1 localhost\n") != string(raw) {
		t.Fatalf("hosts: %s", raw)
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "127.0.0.1 localhost\n" {
		t.Fatalf("hosts after stop: %s", raw)
	}
}

func TestSpliceRejectsOtherNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	f := New(func(ctx context.Context, network, addr string) (net.Conn, error) {
		called <- struct{}{}
		return nil, io.EOF
	})
	f.port = "0"
	f.path = path
	f.flush = false
	if err := f.Sync(true); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	conn, err := net.Dial("tcp", f.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello := helloFor(t, "example.com")
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	_, _ = conn.Read(buf)
	select {
	case <-called:
		t.Fatal("dialed a name outside the list")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStopKeepsListenerWhenHostsStay(t *testing.T) {
	f := New(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, io.EOF
	})
	f.port = "0"
	f.path = t.TempDir()
	f.flush = false
	if err := f.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := f.Stop(); err == nil {
		t.Fatal("expected hosts removal to fail")
	}
	if f.ln == nil {
		t.Fatal("listener closed while the hosts redirect was still in place")
	}
	conn, err := net.Dial("tcp", f.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	f.path = filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(f.path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
}

func helloFor(t *testing.T, name string) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: name, InsecureSkipVerify: true})
		_ = c.Handshake()
		_ = c.Close()
	}()
	_, raw, err := readHello(server)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
