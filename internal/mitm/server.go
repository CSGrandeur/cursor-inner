package mitm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor-inner/internal/cursorpb"

	"cursor-inner/internal/i18n"

	"github.com/elazarl/goproxy"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/procfwd"
	"cursor-inner/internal/protox"
	"cursor-inner/internal/provider"
)

type Server struct {
	mu         sync.Mutex
	ln         net.Listener
	httpSrv    *http.Server
	url        string
	warning    i18n.Text
	directNote i18n.Text
	direct     *procfwd.Forwarder
	proxy      func() config.Proxy
	entries    func() []catalog.Entry
	lookup     func(string) (config.Model, bool)
	hub        *agent.Hub
	client     *http.Client
	running    bool
	selected   string
	catalogOK  bool
	catalogN   int
	catalogAt  time.Time
	localN     int
	officialN  int
	lastError  string
	wsTLS      *tls.Config
}

// Traffic 是顶栏要显示的计数。
type Traffic struct {
	CatalogOK bool
	CatalogN  int
	CatalogAt time.Time
	Local     int
	Official  int
	LastError string
}

func New(proxy func() config.Proxy, entries func() []catalog.Entry, lookup func(string) (config.Model, bool), history *agent.History) *Server {
	s := &Server{proxy: proxy, entries: entries, lookup: lookup, hub: agent.New(lookup, history)}
	s.direct = procfwd.New(s.dialContext)
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
		return "", i18n.E("接管证书缺少解析结果", "Takeover certificate is missing its parsed form")
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
		if currentRawOpen() != nil {
			return &goproxy.ConnectAction{Action: goproxy.ConnectHijack, Hijack: s.recordTunnel}, host
		}
		reportTrace(currentTrace(), TraceEvent{Host: host, Tunnel: true})
		return goproxy.OkConnect, host
	})
	proxy.OnRequest().DoFunc(s.capturePlain)
	proxy.OnResponse().DoFunc(s.finishPlain)
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
	if s.running {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(ctx)
		s.running = false
		s.url = ""
	}
	s.stopDirectLocked()
}

// HoldDirect 曾在删不掉 hosts 标记时继续听 443。当前产品路径不写 hosts，保留空实现以免旧调用方编译失败。
func (s *Server) HoldDirect() {}

func (s *Server) stopDirectLocked() {
	if s.direct != nil {
		s.direct.Stop()
	}
	s.directNote = i18n.Text{}
}

// SyncDirect 不再改系统 hosts，也不听 443。Cursor 走 settings.json 里的本机代理，和 0.2.0 一样。
func (s *Server) SyncDirect() {}

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

func (s *Server) Warning() i18n.Text {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.directNote.IsZero() {
		return s.directNote
	}
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
	if websocketUpgrade(r) {
		s.bridgeWebSocket(w, r)
		return
	}
	fn := currentTrace()
	if fn == nil {
		s.route(w, r)
		return
	}
	var tee *bodyTee
	if r.Body != nil {
		tee = &bodyTee{rc: r.Body}
		r.Body = tee
	}
	tw := &traceWriter{ResponseWriter: w}
	s.route(tw, r)
	var reqBody []byte
	if tee != nil {
		reqBody = tee.buf.Bytes()
	}
	reportTrace(fn, TraceEvent{
		Host:       r.Host,
		Method:     r.Method,
		Path:       r.URL.Path,
		RawQuery:   r.URL.RawQuery,
		Status:     tw.status,
		ReqHeader:  r.Header,
		RespHeader: tw.Header(),
		ReqBody:    reqBody,
		RespBody:   tw.body.Bytes(),
	})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/aiserver.v1.AiService/AvailableModels":
		s.catalog(w, r, true)
	case "/agent.v1.AgentService/GetUsableModels", "/aiserver.v1.AiService/GetUsableModels":
		s.catalog(w, r, false)
	case "/aiserver.v1.BidiService/BidiAppend":
		s.bidi(w, r)
	case "/agent.v1.AgentService/RunSSE":
		s.runSSE(w, r)
	case "/aiserver.v1.CmdKService/StreamCmdK", "/aiserver.v1.CmdKService/StreamTerminalCmdK", "/aiserver.v1.AiService/SlashEdit":
		s.inline(w, r)
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
		s.note(i18n.T("没有自定义模型可追加", "No custom models to add"))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.do(ctx, r, body, true)
	if err != nil {
		s.note(i18n.T("官方目录没有取到，没有改写列表", "Could not fetch the official catalog; model list left unchanged"))
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	upstream, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.note(i18n.T("官方目录原样返回，没有追加自定义模型", "Official catalog returned as-is; custom models not added"))
		writeUpstream(w, resp, upstream)
		return
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		s.note(i18n.T("官方目录带 HTTP 压缩，没有追加自定义模型", "Official catalog uses HTTP compression; custom models not added"))
		writeUpstream(w, resp, upstream)
		return
	}
	merged, ok := protox.MergePlain(upstream, extra)
	if !ok {
		s.note(i18n.T("官方目录帧无法追加自定义模型", "Could not append custom models to the official catalog frame"))
		writeUpstream(w, resp, upstream)
		return
	}
	if len(entries) > 0 {
		s.note(i18n.Tf("已向 Cursor 模型列表追加 %d 个自定义模型", "Added %d custom models to Cursor's model list", len(entries)))
		s.markCatalog(len(entries))
		slog.Debug("追加自定义模型", "path", r.URL.Path, "count", len(entries))
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
		slog.Debug("BidiAppend 解析失败，转发官方", "error", err)
	} else if route.ModelID != "" {
		slog.Debug("BidiAppend", "request", route.RequestID, "model", route.ModelID, "where", where(route.Local))
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
		slog.Debug("RunSSE 解析失败，转发官方", "error", err)
		withBody(r, body)
		s.forward(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	session, err := s.hub.Wait(ctx, id)
	cancel()
	if err != nil {
		slog.Debug("RunSSE 等不到 BidiAppend，转发官方", "request", id, "error", err)
	}
	if err != nil || session == nil {
		withBody(r, body)
		s.forward(w, r)
		return
	}
	slog.Debug("RunSSE 由本地模型回答", "request", id, "model", session.Model.DisplayName)
	s.countLocal()
	defer s.hub.Done(id)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("connect-protocol-version", "1")
	w.WriteHeader(http.StatusOK)
	flush(w)
	d, err := dialer.ForModel(s.proxy(), session.Model.UseProxy)
	session.Web = s.dialContext
	if err == nil {
		err = session.Run(r.Context(), d, func(m *cursorpb.AgentServerMessage) error {
			raw, err := proto.Marshal(m)
			if err != nil {
				return err
			}
			if _, err := w.Write(protox.Frame(0, raw)); err != nil {
				return err
			}
			flush(w)
			return nil
		})
	}
	if err != nil {
		if d == nil {
			name := session.Model.DisplayName
			if name == "" {
				name = session.Model.ID
			}
			slog.Error("✗ "+name, "reason", provider.Explain(err), "error", err)
		}
		slog.Debug("本地模型出错", "request", id, "error", err)
		s.setLastError(provider.Explain(err))
		_, _ = w.Write(protox.EndError(err.Error()))
		flush(w)
		return
	}
	_, _ = w.Write(protox.EndStream())
	flush(w)
}

func displayName(entries []catalog.Entry, id string) string {
	for _, entry := range entries {
		if entry.ID == id && entry.DisplayName != "" {
			return entry.DisplayName
		}
	}
	return ""
}

func leakedModel(body []byte, entries []catalog.Entry) string {
	for _, entry := range entries {
		if len(entry.ID) >= 8 && bytes.Contains(body, []byte(entry.ID)) {
			return entry.ID
		}
	}
	return ""
}

// scrubCustomIDs 用等长占位替换自定义模型 id，避免改动 protobuf 字段长度。
func scrubCustomIDs(body []byte, entries []catalog.Entry) []byte {
	out := body
	for _, entry := range entries {
		if len(entry.ID) < 8 {
			continue
		}
		id := []byte(entry.ID)
		if !bytes.Contains(out, id) {
			continue
		}
		out = bytes.ReplaceAll(out, id, bytes.Repeat([]byte{'0'}, len(entry.ID)))
	}
	return out
}

func connectCompressed(body []byte) bool {
	if len(body) < 5 || body[0]&0x01 == 0 || body[0]&0x02 != 0 {
		return false
	}
	n := binary.BigEndian.Uint32(body[1:5])
	return int(n) == len(body)-5
}

// scrubOutbound 在遥测 / 提示类请求离开本机前去掉自定义模型 id。
// 先记住选中模型（Cmd+K 等仍要用），再改写出站字节。
func scrubOutbound(body []byte, encoding string, entries []catalog.Entry) (next []byte, nextEnc string, changed bool) {
	plain, err := protox.Plain(body, encoding)
	if err != nil || leakedModel(plain, entries) == "" {
		return body, encoding, false
	}
	scrubbed := scrubCustomIDs(plain, entries)
	enc := strings.ToLower(strings.TrimSpace(encoding))
	switch {
	case enc == "gzip" || enc == "x-gzip":
		return scrubbed, "", true
	case connectCompressed(body):
		return protox.Frame(0, scrubbed), encoding, true
	default:
		return scrubCustomIDs(body, entries), encoding, true
	}
}

// leakScanLimit 是外发检查读取的请求体上限；更长的请求体只检查开头，其余部分原样流式转发。
const leakScanLimit = 1 << 20

func (s *Server) warnCustomModel(path string, body []byte, encoding string) {
	if plain, err := protox.Plain(body, encoding); err == nil {
		body = plain
	}
	id := leakedModel(body, s.entries())
	s.rememberModel(path, id)
	if id == "" {
		return
	}
	if quietLeak(path) {
		slog.Debug("转发给官方的请求带有自定义模型", "path", path, "model", id)
		return
	}
	slog.Warn("转发给官方的请求带有自定义模型", "path", path, "model", id, "name", displayName(s.entries(), id))
}

func (s *Server) rememberModel(path, id string) {
	if id == "" && !strings.Contains(path, "GetDefaultModelNudgeData") {
		return
	}
	s.mu.Lock()
	s.selected = id
	s.mu.Unlock()
}

func (s *Server) selectedModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected
}

func quietLeak(path string) bool {
	for _, part := range []string{
		"NameTab",
		"GetUsageLimitStatusAndActiveGrants",
		"AnalyticsService/",
		"GetDefaultModelNudgeData",
		"GetNewChatNudge",
	} {
		if strings.Contains(path, part) {
			return true
		}
	}
	return false
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	s.countOfficial()
	if r.Body != nil {
		head, _ := io.ReadAll(io.LimitReader(r.Body, leakScanLimit))
		enc := r.Header.Get("Content-Encoding")
		s.warnCustomModel(r.URL.Path, head, enc)
		if quietLeak(r.URL.Path) {
			if next, nextEnc, ok := scrubOutbound(head, enc, s.entries()); ok {
				head = next
				if nextEnc == "" {
					r.Header.Del("Content-Encoding")
				} else {
					r.Header.Set("Content-Encoding", nextEnc)
				}
				r.ContentLength = int64(len(head))
				r.Header.Set("Content-Length", strconv.Itoa(len(head)))
				r.Body = io.NopCloser(bytes.NewReader(head))
			} else {
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), r.Body))
			}
		} else {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), r.Body))
		}
	}
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

func (s *Server) markCatalog(n int) {
	s.mu.Lock()
	s.catalogOK = true
	s.catalogN = n
	s.catalogAt = time.Now()
	s.mu.Unlock()
}

func (s *Server) countLocal() {
	s.mu.Lock()
	s.localN++
	s.mu.Unlock()
}

func (s *Server) countOfficial() {
	s.mu.Lock()
	s.officialN++
	s.mu.Unlock()
}

func (s *Server) setLastError(text string) {
	text = strings.TrimSpace(text)
	if len(text) > 80 {
		text = text[:80]
	}
	s.mu.Lock()
	s.lastError = text
	s.mu.Unlock()
}

// Traffic 返回顶栏计数的一份副本。
func (s *Server) Traffic() Traffic {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Traffic{
		CatalogOK: s.catalogOK,
		CatalogN:  s.catalogN,
		CatalogAt: s.catalogAt,
		Local:     s.localN,
		Official:  s.officialN,
		LastError: s.lastError,
	}
}

func (s *Server) note(text i18n.Text) {
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
