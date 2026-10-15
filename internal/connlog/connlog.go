// Package connlog 让拨号方把这条连接最终走了哪条路（代理/直连）告诉调用方，只用于调试日志。
// 记录挂在 context 上，拨号层不用改返回值，也不用包装 net.Conn（包装会丢 CloseWrite）。
package connlog

import (
	"context"
	"sync"
)

// Note 是一条连接的路由记录。拨号可能在首包到达后才发生（仅官方的按首包分流），所以要加锁。
type Note struct {
	mu    sync.Mutex
	route string
}

type key struct{}

// With 返回挂上新记录的 context。
func With(ctx context.Context) (context.Context, *Note) {
	n := &Note{}
	return context.WithValue(ctx, key{}, n), n
}

// Set 记下实际拨号走的路；ctx 上没有记录时什么也不做。多次拨号以最后一次为准。
func Set(ctx context.Context, route string) {
	if n, ok := ctx.Value(key{}).(*Note); ok {
		n.mu.Lock()
		n.route = route
		n.mu.Unlock()
	}
}

// Route 返回记下的路；还没拨号时为空。
func (n *Note) Route() string {
	if n == nil {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.route
}
