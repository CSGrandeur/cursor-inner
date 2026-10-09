package procfwd

import (
	"io"
	"sync"
)

// Tap 在为 nil 时，子进程直连的转发与没挂上时相同。
type Tap struct {
	Open func(host, via string) (toNet, fromNet io.WriteCloser)
	Fail func(host, via, reason string)
}

var (
	tapMu sync.Mutex
	tap   Tap
)

// SetTap 挂上原始字节记录。转发的目标、拨号和关闭顺序不变。
func SetTap(next Tap) {
	tapMu.Lock()
	tap = next
	tapMu.Unlock()
}

func currentTap() Tap {
	tapMu.Lock()
	defer tapMu.Unlock()
	return tap
}

func openTap(host, via string) (io.WriteCloser, io.WriteCloser) {
	fn := currentTap().Open
	if fn == nil {
		return nil, nil
	}
	return fn(host, via)
}

func noteTap(host, via, reason string) {
	fn := currentTap().Fail
	if fn == nil || reason == "" {
		return
	}
	fn(host, via, reason)
}

func closeTap(w io.WriteCloser) {
	if w != nil {
		_ = w.Close()
	}
}

func tapReader(src io.Reader, tee io.Writer) io.Reader {
	if tee == nil {
		return src
	}
	return io.TeeReader(src, steadyWriter{tee})
}

// steadyWriter 吞掉记录失败，避免日志写不进去时中断 Cursor 的连接。
type steadyWriter struct{ w io.Writer }

func (s steadyWriter) Write(p []byte) (int, error) {
	rest := p
	for len(rest) > 0 && s.w != nil {
		n, err := s.w.Write(rest)
		rest = rest[n:]
		if err != nil || n == 0 {
			break
		}
	}
	return len(p), nil
}
