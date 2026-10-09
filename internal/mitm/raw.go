package mitm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"cursor-inner/internal/procfwd"

	"github.com/elazarl/goproxy"
)

type rawLog struct {
	open func(host, via string) (toNet, fromNet io.WriteCloser)
	fail func(host, via, reason string)
}

var (
	rawMu sync.Mutex
	raw   rawLog
)

// SetRaw 挂上未解密连接的原始字节记录。open 为 nil 时，这些连接仍按原来的隧道转发。
func SetRaw(open func(host, via string) (toNet, fromNet io.WriteCloser), fail func(host, via, reason string)) {
	rawMu.Lock()
	raw = rawLog{open: open, fail: fail}
	rawMu.Unlock()
	procfwd.SetTap(procfwd.Tap{Open: open, Fail: fail})
}

func currentRawOpen() func(string, string) (io.WriteCloser, io.WriteCloser) {
	rawMu.Lock()
	defer rawMu.Unlock()
	return raw.open
}

func currentRawFail() func(string, string, string) {
	rawMu.Lock()
	defer rawMu.Unlock()
	return raw.fail
}

func (s *Server) recordTunnel(req *http.Request, client net.Conn, _ *goproxy.ProxyCtx) {
	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	host = hostWithDefaultPort(host)
	up, err := s.dialContext(context.Background(), "tcp", host)
	if err != nil {
		if fail := currentRawFail(); fail != nil {
			fail(host, "proxy", err.Error())
		}
		msg := err.Error()
		_, _ = fmt.Fprintf(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(msg), msg)
		_ = client.Close()
		return
	}
	if _, err = io.WriteString(client, "HTTP/1.0 200 Connection established\r\n\r\n"); err != nil {
		_ = up.Close()
		_ = client.Close()
		return
	}
	var toNet, fromNet io.WriteCloser
	if open := currentRawOpen(); open != nil {
		toNet, fromNet = open(host, "proxy")
	}
	splice(client, up, toNet, fromNet)
}

func hostWithDefaultPort(host string) string {
	if i := strings.LastIndex(host, ":"); i > strings.LastIndex(host, "]") {
		return host
	}
	return host + ":80"
}

type halfClosable interface {
	net.Conn
	CloseWrite() error
	CloseRead() error
}

func splice(client, up net.Conn, toNet, fromNet io.WriteCloser) {
	if dst, ok := up.(halfClosable); ok {
		if src, ok := client.(halfClosable); ok {
			var wg sync.WaitGroup
			wg.Add(2)
			go copyHalf(dst, src, toNet, &wg)
			go copyHalf(src, dst, fromNet, &wg)
			wg.Wait()
			_ = src.Close()
			_ = dst.Close()
			return
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		_, _ = io.Copy(up, tapReader(client, toNet))
		closeWriter(toNet)
		_ = up.Close()
		wg.Done()
	}()
	go func() {
		_, _ = io.Copy(client, tapReader(up, fromNet))
		closeWriter(fromNet)
		_ = client.Close()
		wg.Done()
	}()
	wg.Wait()
}

func copyHalf(dst, src halfClosable, tee io.WriteCloser, wg *sync.WaitGroup) {
	defer wg.Done()
	_, _ = io.Copy(dst, tapReader(src, tee))
	closeWriter(tee)
	_ = dst.CloseWrite()
	_ = src.CloseRead()
}

func closeWriter(w io.WriteCloser) {
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

type plainCap struct {
	host   string
	method string
	path   string
	query  string
	header http.Header
	tee    *bodyTee
}

func (s *Server) capturePlain(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if currentTrace() == nil {
		return req, nil
	}
	cap := &plainCap{
		host: req.Host, method: req.Method, path: req.URL.Path, query: req.URL.RawQuery, header: req.Header,
	}
	if req.Body != nil {
		cap.tee = &bodyTee{rc: req.Body}
		req.Body = cap.tee
	}
	ctx.UserData = cap
	return req, nil
}

func (s *Server) finishPlain(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	cap, _ := ctx.UserData.(*plainCap)
	if cap == nil || resp == nil {
		return resp
	}
	fn := currentTrace()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		reportPlain(fn, cap, resp, nil)
		return resp
	}
	if resp.Body == nil {
		reportPlain(fn, cap, resp, nil)
		return resp
	}
	resp.Body = &finishBody{ReadCloser: resp.Body, done: func(body []byte) {
		reportPlain(fn, cap, resp, body)
	}}
	return resp
}

func reportPlain(fn Trace, cap *plainCap, resp *http.Response, body []byte) {
	var reqBody []byte
	if cap.tee != nil {
		reqBody = cap.tee.buf.Bytes()
	}
	reportTrace(fn, TraceEvent{
		Host: cap.host, Method: cap.method, Path: cap.path, RawQuery: cap.query, Status: resp.StatusCode,
		ReqHeader: cap.header, RespHeader: resp.Header, ReqBody: reqBody, RespBody: body,
	})
}

type finishBody struct {
	io.ReadCloser
	buf  bytes.Buffer
	once sync.Once
	done func([]byte)
}

func (b *finishBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.buf.Write(p[:n])
	}
	return n, err
}

func (b *finishBody) Close() error {
	b.once.Do(func() {
		if b.done != nil {
			b.done(b.buf.Bytes())
		}
	})
	return nil
}
