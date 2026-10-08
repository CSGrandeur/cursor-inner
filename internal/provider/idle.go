package provider

import (
	"io"
	"time"
)

// idleReader 在一次 Read 超过 idle 没有返回时关闭底层并报超时。
type idleReader struct {
	r    io.Reader
	idle time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.r.Read(p)
		done <- result{n, err}
	}()
	timer := time.NewTimer(r.idle)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.n, res.err
	case <-timer.C:
		if c, ok := r.r.(io.Closer); ok {
			_ = c.Close()
		}
		<-done
		return 0, timeoutError()
	}
}
