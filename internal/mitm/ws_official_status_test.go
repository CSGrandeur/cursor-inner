package mitm

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

func wsTestServer() *Server {
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return []catalog.Entry{{ID: "mine"}} },
		func(id string) (config.Model, bool) {
			if id != "mine" {
				return config.Model{}, false
			}
			return config.Model{ID: "mine", Type: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}, true
		}, nil)
	s.wsTLS = &tls.Config{InsecureSkipVerify: true}
	return s
}

func wsUpgradeReq(addr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "https://"+addr+"/agent/v1/run", nil)
	req.Host = addr
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return req
}

// 官方限流（429）时客户端必须拿到官方原始响应，不能被伪造成本地 101。
func TestWebSocketOfficialRateLimitIsNotFaked(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 429 Too Many Requests\r\nContent-Length: 4\r\n\r\nslow")
	}()
	left, right := net.Pipe()
	defer left.Close()
	s := wsTestServer()
	go s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, wsUpgradeReq(ln.Addr().String()))
	_ = left.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, err := bufio.NewReader(left).ReadString('\n')
	if err != nil || !strings.Contains(status, "429") {
		t.Fatalf("客户端应收到官方 429，实际 %q %v", status, err)
	}
}

// 官方 hello 晚到（超过 3 秒）时不能先伪造本地 hello，否则客户端收到两个 hello。
func TestWebSocketLateOfficialHelloIsNotDuplicated(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hello := []byte{0x0a, 0x02, 0x08, 0x01}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if _, _, _, err := readWSFrame(br); err != nil {
			return
		}
		time.Sleep(3500 * time.Millisecond)
		_ = writeWSFrame(conn, 0x2, hello)
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
		for {
			if _, _, _, err := readWSFrame(br); err != nil {
				return
			}
		}
	}()
	left, right := net.Pipe()
	defer left.Close()
	s := wsTestServer()
	go s.bridgeWebSocket(&hijackRW{Conn: right, header: make(http.Header)}, wsUpgradeReq(ln.Addr().String()))
	br := bufio.NewReader(left)
	_ = left.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := left.Write(maskWSFrame(hello)); err != nil {
		t.Fatal(err)
	}
	// 用户在官方模型上发出这一轮：客户端紧接着发 run 的帧。
	meta, run := agentRunFrames(t, "run-1", "req-1", "grok-test", "ping-official")
	go func() {
		time.Sleep(3200 * time.Millisecond)
		_, _ = left.Write(maskWSFrame(meta))
		_, _ = left.Write(maskWSFrame(run))
	}()
	hellos := 0
	_ = left.SetReadDeadline(time.Now().Add(6 * time.Second))
	for {
		_, payload, _, err := readWSFrame(br)
		if err != nil {
			break
		}
		if string(payload) == string(hello) {
			hellos++
		}
	}
	if hellos != 1 {
		t.Fatalf("客户端应只收到 1 个 hello（官方的），实际 %d 个", hellos)
	}
}
