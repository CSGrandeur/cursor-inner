package mitm

import (
	"net"
	"sync"
	"sync/atomic"
)

// connCounter 数还没关闭的连接，含被劫持的 CONNECT 隧道。
type connCounter struct{ n atomic.Int64 }

func (c *connCounter) wrap(ln net.Listener) net.Listener { return countingListener{ln, c} }

type countingListener struct {
	net.Listener
	c *connCounter
}

func (l countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.c.n.Add(1)
	return &countedConn{Conn: conn, c: l.c}, nil
}

type countedConn struct {
	net.Conn
	c    *connCounter
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.c.n.Add(-1) })
	return c.Conn.Close()
}
