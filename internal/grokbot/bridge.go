package grokbot

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/selfupdate"
)

// DialFunc 与 dialer.Func 同形，避免 grokbot 依赖 dialer。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// NeedsBridge 报告这个代理地址 Grok Bot 自己用不了：Chromium 和 undici 都不认
// --proxy-server / HTTPS_PROXY 里的 user:pass，也不会对 socks 做 HTTP CONNECT。
// 这时由 cursor-inner 在回环地址上开一个 HTTP CONNECT 桥，出站经配置的代理（含认证）拨号。
func NeedsBridge(spec string) bool {
	spec = strings.TrimSpace(spec)
	if !strings.Contains(spec, "://") {
		spec = "http://" + spec
	}
	u, err := url.Parse(spec)
	if err != nil {
		return false
	}
	return u.User != nil || strings.HasPrefix(strings.ToLower(u.Scheme), "socks")
}

// Bridge 是只监听 127.0.0.1 的 HTTP 代理。所有出站都走 dial；dial 失败就回 502，绝不直连。
// 端口按代理地址算出一个固定值优先绑定，cursor-inner 重启后 Grok Bot 的启动参数仍然有效。
type Bridge struct {
	mu   sync.Mutex
	ln   net.Listener
	dial DialFunc
	key  string

	released string
}

// Ensure 让桥按 key（代理地址）和 dial 运行，返回给 Grok Bot 用的 http://127.0.0.1:端口。
func (b *Bridge) Ensure(key string, dial DialFunc) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dial = dial
	if b.ln != nil && b.key == key {
		return "http://" + b.ln.Addr().String(), nil
	}
	if b.ln != nil {
		_ = b.ln.Close()
		b.ln = nil
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", stablePort(key)))
	if err != nil {
		ln, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
	}
	b.ln, b.key = ln, key
	go b.serve(ln)
	return "http://" + ln.Addr().String(), nil
}

// Close 停掉桥。之后 Grok Bot 连不上代理，但不会改成直连。
func (b *Bridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln != nil {
		_ = b.ln.Close()
		b.ln = nil
	}
}

// Release 停止接受新连接（已建立的连接继续），记住地址供 Reclaim。自更新交接用。
func (b *Bridge) Release() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln == nil {
		return nil
	}
	b.released = b.ln.Addr().String()
	err := b.ln.Close()
	b.ln = nil
	return err
}

// Reclaim 在交出的地址上重新监听（交接失败回滚）。
func (b *Bridge) Reclaim() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released == "" || b.ln != nil {
		return nil
	}
	addr := b.released
	var ln net.Listener
	err := selfupdate.Rebind(context.Background(), func() error {
		l, err := net.Listen("tcp4", addr)
		if err == nil {
			ln = l
		}
		return err
	})
	if err != nil {
		return err
	}
	b.ln, b.released = ln, ""
	go b.serve(ln)
	return nil
}

func stablePort(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte("cursor-inner-grok-bridge\n" + key))
	return 20000 + int(h.Sum32()%20000)
}

func (b *Bridge) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go b.handle(c)
	}
}

func (b *Bridge) currentDial() DialFunc {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dial
}

func (b *Bridge) handle(c net.Conn) {
	defer c.Close()
	if tcp, ok := c.RemoteAddr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	dial := b.currentDial()
	if dial == nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target := req.Host
	if req.Method != http.MethodConnect {
		target = req.URL.Host
		if req.URL.Port() == "" {
			target = net.JoinHostPort(req.URL.Hostname(), "80")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	up, err := dial(ctx, "tcp", target)
	cancel()
	if err != nil {
		slog.Warn("Grok 代理桥没能经代理连上目标，已拒绝（不会直连）", "target", target, "error", err.Error())
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	if req.Method == http.MethodConnect {
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if n := br.Buffered(); n > 0 {
			rest, _ := br.Peek(n)
			if _, err := up.Write(rest); err != nil {
				return
			}
		}
		pipe(c, up)
		return
	}
	req.RequestURI = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.Close = true
	if err := req.Write(up); err != nil {
		return
	}
	_, _ = io.Copy(c, up)
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
