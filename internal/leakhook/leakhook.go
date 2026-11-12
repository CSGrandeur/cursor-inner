// Package leakhook 把一次泄漏检查挂进来，供设置页按需触发。
// 未挂入时设置页的“泄漏检查”按钮会提示此构建未启用。
package leakhook

import "sync"

var (
	mu sync.Mutex
	fn func() error
)

// Set 挂上触发函数。传 nil 等于摘掉。
func Set(f func() error) {
	mu.Lock()
	fn = f
	mu.Unlock()
}

// Enabled 表示已经挂上。
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return fn != nil
}

// Trigger 跑一次。没挂时返回 nil，什么都不做。
func Trigger() error {
	mu.Lock()
	f := fn
	mu.Unlock()
	if f == nil {
		return nil
	}
	return f()
}
