package dialer

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"cursor-inner/internal/config"
)

// 经代理拨号失败要通知看护（复查代理是否还在），且绝不改成直连。
func TestProxyDialFailureNotifiesAndNeverGoesDirect(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	var n atomic.Int64
	OnProxyDialError(func() { n.Add(1) })
	t.Cleanup(func() { proxyFailHook.Store(nil) })
	for _, spec := range []string{"socks5://" + addr, "http://" + addr} {
		d, err := FromProxy(config.Proxy{Enabled: true, Address: spec})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, err := d(ctx, "tcp", "api2.cursor.sh:443")
		cancel()
		if err == nil {
			c.Close()
			t.Fatalf("%s: dial through a closed proxy must fail, not go direct", spec)
		}
	}
	if n.Load() != 2 {
		t.Fatalf("hook calls=%d want 2", n.Load())
	}
}
