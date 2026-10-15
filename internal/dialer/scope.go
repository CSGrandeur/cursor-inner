package dialer

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/connlog"
	"cursor-inner/internal/official"
)

// Scoped 返回 Cursor 和 Grok Bot 流量用的拨号。代理范围是全局时与 FromProxy 相同；
// 「仅官方」时按名字分流：名单里的域名、fake-ip 段、没有域名的公网 IP 走代理，回环和私网直连。
// 其它域名先看 TLS SNI / HTTP Host；首包里没有主机名就用拨号目标上的名字。
// 自定义模型和 cursor-inner 自己的联网不用它，各看模型自己的「走代理」。
func Scoped(p config.Proxy) (Func, error) {
	via, err := FromProxy(p)
	if err != nil {
		return nil, err
	}
	_, on, _ := EffectiveAddress(p)
	if !on {
		return marked(via, "direct"), nil
	}
	via = marked(via, "proxy")
	direct := marked(directDial(), "direct")
	if !p.OfficialOnly() {
		return reservedFirst(via, direct), nil
	}
	return reservedFirst(split(via, direct), direct), nil
}

// marked 在拨号时把实际走的路记到 ctx 上（connlog），供代理桥的调试日志使用。
func marked(d Func, route string) Func {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		connlog.Set(ctx, route)
		return d(ctx, network, addr)
	}
}

// lookupIP 是判断主机名是否落在内网预留段用的解析，测试里换成假的。
var lookupIP = net.DefaultResolver.LookupNetIP

// lookupWait 是这次本地解析最多等多久。超时或失败按原来的规则走（全局即走代理）。
var lookupWait = 2 * time.Second

// reservedFirst 让两种代理范围都先放行内网：目标是内网预留 IP，或主机名在本地解析后全部落在
// 内网预留段，就直连解析出的地址（不再二次解析）。官方域名不解析、不看结果，一律交给 next，
// 防 DNS 重绑定把官方流量引到直连。解析出任何一个公网地址也交给 next。
func reservedFirst(next, direct Func) Func {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return next(ctx, network, addr)
		}
		if official.MustProxy(host) {
			return next(ctx, network, addr)
		}
		if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			if official.Reserved(ip) {
				slog.Debug("内网预留地址：直连", "target", addr)
				return direct(ctx, network, addr)
			}
			return next(ctx, network, addr)
		}
		lctx, cancel := context.WithTimeout(ctx, lookupWait)
		ips, err := lookupIP(lctx, "ip", host)
		cancel()
		if err != nil || len(ips) == 0 {
			return next(ctx, network, addr)
		}
		for _, ip := range ips {
			if !official.Reserved(ip) {
				return next(ctx, network, addr)
			}
		}
		target := net.JoinHostPort(ips[0].Unmap().String(), port)
		slog.Debug("主机名解析到内网预留段：直连", "target", addr, "ip", target)
		return direct(ctx, network, target)
	}
}

// directDial 是「仅官方」时的直连拨号，测试里换成记录用的假拨号。
var directDial = Direct

// inspectWait 是等 TLS SNI 或 HTTP Host 的时间。到点还没有，就用拨号目标上的名字。
// 名单里的域名在拨号时已经走代理，不会进到这里。
var inspectWait = 3 * time.Second

func split(via, direct Func) Func {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		switch route := official.ForHost(host); route {
		case official.Direct:
			slog.Debug("代理范围仅官方：直连", "target", addr, "why", "loopback/private")
			return direct(ctx, network, addr)
		case official.Proxy:
			slog.Debug("代理范围仅官方：走代理", "target", addr)
			return via(ctx, network, addr)
		}
		return newInspectConn(ctx, network, addr, via, direct), nil
	}
}

// inspectConn 先把客户端写来的数据存着，认出 SNI / Host 再拨号：确认不是官方的直连，其余走代理。
// 拨号失败时后续读写都返回这个错误，绝不改走另一条路。
type inspectConn struct {
	network, addr string
	via, direct   Func
	ctx           context.Context

	wmu sync.Mutex // 串行化写入与首包冲刷，保证字节顺序

	mu       sync.Mutex
	buf      []byte
	decided  bool
	conn     net.Conn
	err      error
	closed   bool
	ready    chan struct{}
	timer    *time.Timer
	rdl, wdl time.Time
}

func newInspectConn(ctx context.Context, network, addr string, via, direct Func) *inspectConn {
	c := &inspectConn{network: network, addr: addr, via: via, direct: direct,
		ctx: context.WithoutCancel(ctx), ready: make(chan struct{})}
	c.mu.Lock()
	c.timer = time.AfterFunc(inspectWait, func() {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		route, name := c.dialRoute()
		c.decide(route, name, "no client data")
	})
	c.mu.Unlock()
	return c
}

// dialRoute 用拨号目标上的名字决定。名单以外的域名直连；没有域名的公网 IP 走代理。
func (c *inspectConn) dialRoute() (official.Route, string) {
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		host = c.addr
	}
	if r := official.ForHost(host); r != official.Inspect {
		return r, host
	}
	return official.Direct, host
}

// decide 拨号并冲刷已缓存的数据。调用方必须持有 wmu。
// route 为 ByDial 时改用拨号目标上的名字。
func (c *inspectConn) decide(route official.Route, name, why string) {
	c.mu.Lock()
	if c.decided {
		c.mu.Unlock()
		return
	}
	if route == official.ByDial {
		route, name = c.dialRoute()
		why = "dial name"
	}
	c.decided = true
	c.timer.Stop()
	if c.closed {
		c.err = net.ErrClosed
		c.mu.Unlock()
		close(c.ready)
		return
	}
	pending := c.buf
	c.buf = nil
	c.mu.Unlock()

	dial, label := c.via, "proxy"
	if route == official.Direct {
		dial, label = c.direct, "direct"
	}
	slog.Debug("代理范围仅官方：按首包分流", "target", c.addr, "route", label, "name", name, "why", why)
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	conn, err := dial(ctx, c.network, c.addr)
	cancel()
	if err == nil && len(pending) > 0 {
		if _, werr := conn.Write(pending); werr != nil {
			conn.Close()
			conn, err = nil, werr
		}
	}
	c.mu.Lock()
	switch {
	case err != nil:
		c.err = err
	case c.closed:
		conn.Close()
		c.err = net.ErrClosed
	default:
		c.conn = conn
		if !c.rdl.IsZero() {
			_ = conn.SetReadDeadline(c.rdl)
		}
		if !c.wdl.IsZero() {
			_ = conn.SetWriteDeadline(c.wdl)
		}
	}
	c.mu.Unlock()
	close(c.ready)
}

func (c *inspectConn) current() (net.Conn, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn, c.err, c.decided
}

func (c *inspectConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if conn, err, decided := c.current(); decided {
		if err != nil {
			return 0, err
		}
		return conn.Write(p)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	c.buf = append(c.buf, p...)
	route, name, done := official.InspectFirst(c.buf)
	big := len(c.buf) >= official.MaxInspect
	c.mu.Unlock()
	if !done && !big {
		return len(p), nil
	}
	why := "first bytes"
	if !done {
		route, name = c.dialRoute()
		why = "too long to inspect"
	}
	c.decide(route, name, why)
	if _, err, _ := c.current(); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *inspectConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	dl := c.rdl
	c.mu.Unlock()
	if dl.IsZero() {
		<-c.ready
	} else {
		t := time.NewTimer(time.Until(dl))
		select {
		case <-c.ready:
			t.Stop()
		case <-t.C:
			return 0, os.ErrDeadlineExceeded
		}
	}
	conn, err, _ := c.current()
	if err != nil {
		return 0, err
	}
	return conn.Read(p)
}

func (c *inspectConn) Close() error {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	decided := c.decided
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	if !decided {
		// 还没拨号：交给 decide 收尾（它看到 closed 就不拨了）。
		go func() {
			c.wmu.Lock()
			defer c.wmu.Unlock()
			c.decide(official.Proxy, "", "closed")
		}()
	}
	return nil
}

// CloseWrite：客户端说完了还没有主机名，就用拨号目标上的名字，再半关。
func (c *inspectConn) CloseWrite() error {
	c.wmu.Lock()
	if _, _, decided := c.current(); !decided {
		c.mu.Lock()
		route, name, done := official.InspectFirst(c.buf)
		c.mu.Unlock()
		if !done {
			route, name = c.dialRoute()
		}
		c.decide(route, name, "client closed write")
	}
	c.wmu.Unlock()
	conn, err, _ := c.current()
	if err != nil {
		return err
	}
	if hc, ok := conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return nil
}

func (c *inspectConn) CloseRead() error {
	conn, _, _ := c.current()
	if hc, ok := conn.(interface{ CloseRead() error }); ok {
		return hc.CloseRead()
	}
	return nil
}

var placeholder = &net.TCPAddr{IP: net.IPv4zero}

func (c *inspectConn) LocalAddr() net.Addr {
	if conn, _, _ := c.current(); conn != nil {
		return conn.LocalAddr()
	}
	return placeholder
}

func (c *inspectConn) RemoteAddr() net.Addr {
	if conn, _, _ := c.current(); conn != nil {
		return conn.RemoteAddr()
	}
	return placeholder
}

func (c *inspectConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

func (c *inspectConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdl = t
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		return conn.SetReadDeadline(t)
	}
	return nil
}

func (c *inspectConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wdl = t
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		return conn.SetWriteDeadline(t)
	}
	return nil
}
