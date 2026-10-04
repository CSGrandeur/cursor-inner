package mitm

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
	"github.com/elazarl/goproxy"
)

type Server struct {
	mu      sync.Mutex
	ln      net.Listener
	httpSrv *http.Server
	url     string
	warning string
	proxy   func() config.Proxy
	entries func() []catalog.Entry
	lookup  func(string) (config.Model, bool)
	hub     *agent.Hub
	client  *http.Client
	running bool
}

func New(proxy func() config.Proxy, entries func() []catalog.Entry, lookup func(string) (config.Model, bool)) *Server {
	s := &Server{proxy: proxy, entries: entries, lookup: lookup, hub: agent.New(lookup)}
	s.client = &http.Client{Transport: upstreamTransport(s.dialContext), CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return s
}

func (s *Server) Start(ca tls.Certificate) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.url, nil
	}
	if ca.Leaf == nil && len(ca.Certificate) > 0 {
		return "", errors.New("接管证书缺少解析结果")
	}
	goproxy.GoproxyCa = ca
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.Logger = log.New(io.Discard, "", 0)
	proxy.Tr = upstreamTransport(s.dialContext)
	proxy.ConnectDial = func(network, addr string) (net.Conn, error) {
		return s.dialContext(context.Background(), network, addr)
	}
	proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if cursorHost(host) {
			return &goproxy.ConnectAction{
				Action: goproxy.ConnectHijack,
				Hijack: s.hijack,
			}, host
		}
		return goproxy.OkConnect, host
	})
	srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 20 * time.Second}
	s.ln = ln
	s.httpSrv = srv
	s.url = "http://" + ln.Addr().String()
	s.running = true
	go func() { _ = srv.Serve(ln) }()
	return s.url, nil
}

func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(ctx)
	s.running = false
	s.url = ""
}

func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Server) Warning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

func (s *Server) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d, err := dialer.FromProxy(s.proxy())
	if err != nil {
		return nil, err
	}
	return d(ctx, network, addr)
}

func cursorHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "api2.cursor.sh" || host == "api3.cursor.sh" || strings.HasSuffix(host, ".cursor.sh")
}

func (s *Server) hijack(req *http.Request, client net.Conn, ctx *goproxy.ProxyCtx) {
	if _, err := io.WriteString(client, "HTTP/1.0 200 Connection established\r\n\r\n"); err != nil {
		client.Close()
		return
	}
	tlsCfg, err := goproxy.TLSConfigFromCA(&goproxy.GoproxyCa)(req.Host, ctx)
	if err != nil {
		client.Close()
		return
	}
	tlsCfg.NextProtos = []string{"http/1.1"}
	tlsConn := tls.Server(client, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		client.Close()
		return
	}
	done := make(chan struct{})
	wrapped := &closeOnce{Conn: tlsConn, done: done}
	ln := &onceListener{conn: wrapped, done: done, addr: client.LocalAddr()}
	srv := &http.Server{
		Handler:           http.HandlerFunc(s.serveCursor),
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ReadHeaderTimeout: 20 * time.Second,
	}
	_ = srv.Serve(ln)
}

func (s *Server) serveCursor(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/aiserver.v1.AiService/AvailableModels":
		s.catalog(w, r, true)
	case "/agent.v1.AgentService/GetUsableModels", "/aiserver.v1.AiService/GetUsableModels":
		s.catalog(w, r, false)
	case "/aiserver.v1.BidiService/BidiAppend":
		s.bidi(w, r)
	case "/agent.v1.AgentService/RunSSE":
		s.runSSE(w, r)
	default:
		s.forward(w, r)
	}
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request, available bool) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	entries := s.entries()
	extra := catalog.Usable(entries)
	if available {
		extra = catalog.Available(entries)
	}
	if len(entries) == 0 {
		s.note("没有自定义模型可追加")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.do(ctx, r, body, true)
	if err != nil {
		s.note("官方目录没有取到，没有改写列表")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	upstream, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.note("官方目录原样返回，没有追加自定义模型")
		writeUpstream(w, resp, upstream)
		return
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		s.note("官方目录带 HTTP 压缩，没有追加自定义模型")
		writeUpstream(w, resp, upstream)
		return
	}
	merged, ok := protox.MergePlain(upstream, extra)
	if !ok {
		s.note("官方目录帧无法追加自定义模型")
		writeUpstream(w, resp, upstream)
		return
	}
	if len(entries) > 0 {
		s.note("已向 Cursor 模型列表追加 " + strconv.Itoa(len(entries)) + " 个自定义模型")
		log.Printf("%s 追加 %d 个自定义模型", r.URL.Path, len(entries))
	}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Connect-Content-Encoding")
	writeUpstream(w, resp, merged)
}

func (s *Server) bidi(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	plain, err := protox.Plain(body, r.Header.Get("Content-Encoding"))
	var route agent.Route
	if err == nil {
		route, err = s.hub.Bidi(plain)
	}
	if err != nil {
		log.Printf("BidiAppend 解析失败，转发官方：%v", err)
	} else if route.ModelID != "" {
		log.Printf("BidiAppend %s 模型 %s -> %s", route.RequestID, route.ModelID, where(route.Local))
	}
	if err != nil || !route.Local {
		withBody(r, body)
		s.forward(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/proto")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) runSSE(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	plain, err := protox.Plain(body, r.Header.Get("Content-Encoding"))
	id := ""
	if err == nil {
		id, err = protox.DecodeRunID(plain)
	}
	if err != nil {
		log.Printf("RunSSE 解析失败，转发官方：%v", err)
		withBody(r, body)
		s.forward(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	decision, err := s.hub.Wait(ctx, id)
	cancel()
	if err != nil {
		log.Printf("RunSSE %s 等不到 BidiAppend，转发官方：%v", id, err)
	}
	if err != nil || !decision.Local {
		withBody(r, body)
		s.forward(w, r)
		return
	}
	log.Printf("RunSSE %s 由本地模型 %s 回答", id, decision.Model.DisplayName)
	defer s.hub.Done(id)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("connect-protocol-version", "1")
	w.WriteHeader(http.StatusOK)
	flush(w)
	d, err := dialer.ForModel(s.proxy(), decision.Model.UseProxy)
	if err != nil {
		_, _ = w.Write(protox.EndError(err.Error()))
		flush(w)
		return
	}
	err = provider.Stream(r.Context(), decision.Model, d, decision.Messages, func(text string) error {
		if _, err := w.Write(protox.TextDelta(text)); err != nil {
			return err
		}
		flush(w)
		return nil
	})
	if err != nil {
		log.Printf("RunSSE %s 本地模型出错：%v", id, err)
		_, _ = w.Write(protox.EndError(err.Error()))
		flush(w)
		return
	}
	_, _ = w.Write(protox.TurnEnded())
	_, _ = w.Write(protox.EndStream())
	flush(w)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	resp, err := s.do(r.Context(), r, nil, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flush(w)
		}
		if rerr != nil {
			return
		}
	}
}

func (s *Server) do(ctx context.Context, r *http.Request, body []byte, catalog bool) (*http.Response, error) {
	var reader io.Reader
	length := r.ContentLength
	if body != nil {
		reader = bytes.NewReader(body)
		length = int64(len(body))
	} else {
		reader = r.Body
	}
	out, err := http.NewRequestWithContext(ctx, r.Method, "https://"+requestHost(r)+r.URL.RequestURI(), reader)
	if err != nil {
		return nil, err
	}
	out.Header = r.Header.Clone()
	removeHop(out.Header)
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header["User-Agent"] = []string{""}
	}
	out.ContentLength = length
	out.Proto, out.ProtoMajor, out.ProtoMinor = "HTTP/1.1", 1, 1
	if catalog {
		out.Header.Set("Accept-Encoding", "identity")
		out.Header.Set("Connect-Accept-Encoding", "identity")
	}
	return s.client.Do(out)
}

func where(local bool) string {
	if local {
		return "本地"
	}
	return "官方"
}

func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func (s *Server) note(text string) {
	s.mu.Lock()
	s.warning = text
	s.mu.Unlock()
}

func withBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func upstreamTransport(dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		DialContext:        dial,
		Proxy:              nil,
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		TLSNextProto:       map[string]func(string, *tls.Conn) http.RoundTripper{},
		TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func writeUpstream(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if _, skip := hop[http.CanonicalHeaderKey(k)]; skip {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

var hop = map[string]struct{}{
	"Connection": {}, "Proxy-Connection": {}, "Keep-Alive": {}, "Proxy-Authenticate": {},
	"Proxy-Authorization": {}, "Te": {}, "Trailer": {}, "Transfer-Encoding": {}, "Upgrade": {},
	"Content-Length": {},
}

func removeHop(h http.Header) {
	for k := range hop {
		h.Del(k)
	}
	if c := h.Get("Connection"); c != "" {
		for _, name := range strings.Split(c, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
}

type onceListener struct {
	conn net.Conn
	done chan struct{}
	addr net.Addr
}

func (l *onceListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		c := l.conn
		l.conn = nil
		return c, nil
	}
	<-l.done
	return nil, errors.New("closed")
}

func (l *onceListener) Close() error { return nil }
func (l *onceListener) Addr() net.Addr {
	if l.addr != nil {
		return l.addr
	}
	return dummyAddr{}
}

type dummyAddr struct{}

func (dummyAddr) Network() string { return "tcp" }
func (dummyAddr) String() string  { return "127.0.0.1:0" }

type closeOnce struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *closeOnce) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}
