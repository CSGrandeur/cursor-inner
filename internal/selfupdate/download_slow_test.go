package selfupdate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 限速模拟服务器：按给定速度吐出同一个文件，支持 Range（可关掉），可以在某个位置断开或卡住，可以篡改内容。

type slowServer struct {
	srv     *httptest.Server
	payload []byte
	rate    int // 字节/秒
	noRange bool
	tamper  bool

	mu       sync.Mutex
	requests []string // 每次请求的 Range 头（空串表示整份）
	dropAt   int64    // >0：第一次请求写到这个位置就断开
	stallAt  int64    // >0：第一次请求写到这个位置就不再写（连接不关）
	served   int64
}

func newSlowServer(t *testing.T, payload []byte, rate int) *slowServer {
	s := &slowServer{payload: payload, rate: rate}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *slowServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	first := len(s.requests) == 0
	s.requests = append(s.requests, r.Header.Get("Range"))
	dropAt, stallAt := s.dropAt, s.stallAt
	s.mu.Unlock()
	if s.noRange {
		r.Header.Del("Range")
	}
	body := s.payload
	if s.tamper {
		body = bytes.Clone(s.payload)
		body[len(body)/2] ^= 0xff
	}
	if !first {
		dropAt, stallAt = 0, 0
	}
	tw := &throttled{w: w, rate: s.rate, ctx: r.Context(), dropAt: dropAt, stallAt: stallAt, count: &s.served, mu: &s.mu}
	http.ServeContent(tw, r, "pkg", time.Time{}, bytes.NewReader(body))
}

func (s *slowServer) ranges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *slowServer) bytesServed() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

type throttled struct {
	w       http.ResponseWriter
	rate    int
	ctx     context.Context
	written int64
	dropAt  int64
	stallAt int64
	count   *int64
	mu      *sync.Mutex
}

func (t *throttled) Header() http.Header { return t.w.Header() }
func (t *throttled) WriteHeader(c int)   { t.w.WriteHeader(c) }

func (t *throttled) Write(p []byte) (int, error) {
	const chunk = 4 << 10
	n := 0
	for len(p) > 0 {
		c := min(chunk, len(p))
		if t.dropAt > 0 && t.written+int64(c) > t.dropAt {
			c = int(t.dropAt - t.written)
			if c > 0 {
				t.w.Write(p[:c])
				t.written += int64(c)
			}
			if f, ok := t.w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler) // 中途断开连接
		}
		if t.stallAt > 0 && t.written+int64(c) > t.stallAt {
			if f, ok := t.w.(http.Flusher); ok {
				f.Flush()
			}
			<-t.ctx.Done() // 不再写，也不关连接
			return n, t.ctx.Err()
		}
		select {
		case <-t.ctx.Done():
			return n, t.ctx.Err()
		case <-time.After(time.Duration(float64(c) / float64(t.rate) * float64(time.Second))):
		}
		m, err := t.w.Write(p[:c])
		n += m
		t.written += int64(m)
		t.mu.Lock()
		*t.count += int64(m)
		t.mu.Unlock()
		if f, ok := t.w.(http.Flusher); ok {
			f.Flush()
		}
		if err != nil {
			return n, err
		}
		p = p[c:]
	}
	return n, nil
}

// routeTo 是一条名为 name、不管地址是什么都连到 srv 的路径（模拟「默认网络」和「代理」通向不同速度的同一个文件）。
func routeTo(name string, s *slowServer) Route {
	addr := strings.TrimPrefix(s.srv.URL, "http://")
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	return Route{Name: name, Client: &http.Client{Transport: tr}}
}

func randomPayload(t *testing.T, n int) ([]byte, Asset) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return b, Asset{Name: "cursor-inner-test.exe", Size: int64(n), SHA256: hex.EncodeToString(sum[:])}
}

// 测试用的快节奏策略：0.4s 预热、慢于 1s 剩余就并行比较 0.6s，卡住 0.6s 算断。
var fastPolicy = DownloadPolicy{Warmup: 400 * time.Millisecond, Window: 800 * time.Millisecond, SlowETA: time.Second, Race: 600 * time.Millisecond, Stall: 600 * time.Millisecond, Total: 30 * time.Second, Tick: 50 * time.Millisecond}

const pkgURL = "http://pkg.test/releases/download/v9.9.9/cursor-inner-test.exe"

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("content mismatch (err=%v, len %d vs %d)", err, len(got), len(want))
	}
}

func noLeftovers(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// 默认网络很慢但一直有数据（真实场景：直连 GitHub CDN 约 60KB/s）：并行试代理，代理快就换过去，从断点 Range 续下。
func TestDownloadSlowDefaultSwitchesToProxy(t *testing.T) {
	payload, a := randomPayload(t, 1<<20)
	slow := newSlowServer(t, payload, 64<<10)
	fast := newSlowServer(t, payload, 4<<20)
	var mu sync.Mutex
	var seen []Progress
	dir := t.TempDir()
	start := time.Now()
	path, used, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", slow), Alt: []Route{routeTo("proxy", fast)}, URLs: []string{pkgURL}}}, a, dir, fastPolicy, func(p Progress) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
	})
	elapsed := time.Since(start)
	if err != nil || used != "github" {
		t.Fatalf("used=%s err=%v", used, err)
	}
	checkFile(t, path, payload)
	// 只靠默认网络要 16s；换过去后应在几秒内完成。
	if elapsed > 6*time.Second {
		t.Fatalf("took %s", elapsed)
	}
	r := fast.ranges()
	if len(r) != 1 || !strings.HasPrefix(r[0], "bytes=") || r[0] == "bytes=0-" {
		t.Fatalf("proxy should resume from an offset, got %v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawDefault, sawProxy, sawRate bool
	for _, p := range seen {
		sawDefault = sawDefault || p.Route == "default"
		sawProxy = sawProxy || p.Route == "proxy"
		sawRate = sawRate || p.Rate > 0
		if p.Total != a.Size || p.Done > a.Size {
			t.Fatalf("bad progress %+v", p)
		}
	}
	if !sawDefault || !sawProxy || !sawRate {
		t.Fatalf("progress default=%v proxy=%v rate=%v", sawDefault, sawProxy, sawRate)
	}
	t.Logf("1 MiB: default 64KiB/s alone ≈16s; with switch %s; default served %d bytes, proxy %d", elapsed.Round(10*time.Millisecond), slow.bytesServed(), fast.bytesServed())
}

// 默认网络够快：不碰代理。
func TestDownloadFastDefaultNeverTriesProxy(t *testing.T) {
	payload, a := randomPayload(t, 512<<10)
	def := newSlowServer(t, payload, 8<<20)
	proxy := newSlowServer(t, payload, 8<<20)
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", def), Alt: []Route{routeTo("proxy", proxy)}, URLs: []string{pkgURL}}}, a, t.TempDir(), fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
	if n := len(proxy.ranges()); n != 0 {
		t.Fatalf("proxy hit %d times", n)
	}
}

// 默认网络慢、代理更慢：比过之后留在默认网络，代理请求被取消。
func TestDownloadKeepsDefaultWhenProxyIsSlower(t *testing.T) {
	payload, a := randomPayload(t, 400<<10)
	def := newSlowServer(t, payload, 200<<10)
	proxy := newSlowServer(t, payload, 32<<10)
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", def), Alt: []Route{routeTo("proxy", proxy)}, URLs: []string{pkgURL}}}, a, t.TempDir(), fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
	if n := len(proxy.ranges()); n != 1 {
		t.Fatalf("proxy should be raced exactly once, got %d", n)
	}
	if proxy.bytesServed() > 64<<10 {
		t.Fatalf("slower proxy kept downloading: %d bytes", proxy.bytesServed())
	}
}

// 只有一条路径、连接中途断开：从断点 Range 续传，不重下已下的部分。
func TestDownloadResumesAfterDrop(t *testing.T) {
	payload, a := randomPayload(t, 300<<10)
	s := newSlowServer(t, payload, 8<<20)
	s.dropAt = 100 << 10
	dir := t.TempDir()
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", s), URLs: []string{pkgURL}}}, a, dir, fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
	r := s.ranges()
	if len(r) != 2 || r[0] != "" || r[1] != "bytes=102400-" {
		t.Fatalf("requests %v", r)
	}
	noLeftovers(t, dir)
}

// 默认网络给了一部分就卡住（连接不断、没有数据）：判定卡住，改走代理续传。
func TestDownloadStallSwitchesRoute(t *testing.T) {
	payload, a := randomPayload(t, 300<<10)
	def := newSlowServer(t, payload, 8<<20)
	def.stallAt = 64 << 10
	proxy := newSlowServer(t, payload, 8<<20)
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", def), Alt: []Route{routeTo("proxy", proxy)}, URLs: []string{pkgURL}}}, a, t.TempDir(), fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
	if r := proxy.ranges(); len(r) != 1 || r[0] == "" {
		t.Fatalf("proxy requests %v", r)
	}
}

// 续传时服务器不支持 Range（回 200 整份）：从头覆盖写，结果照样正确。
func TestDownloadResumeWithoutRangeSupport(t *testing.T) {
	payload, a := randomPayload(t, 200<<10)
	s := newSlowServer(t, payload, 8<<20)
	s.dropAt = 50 << 10
	s.noRange = true
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", s), URLs: []string{pkgURL}}}, a, t.TempDir(), fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
}

// 换过去的代理给的是被篡改的内容：拼出来的文件 sha256 对不上，逐条路径单独重下，默认网络那份是好的就用它。
func TestDownloadTamperedAltRouteFallsBackToCleanRoute(t *testing.T) {
	payload, a := randomPayload(t, 256<<10)
	def := newSlowServer(t, payload, 160<<10)
	proxy := newSlowServer(t, payload, 4<<20)
	proxy.tamper = true
	dir := t.TempDir()
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", def), Alt: []Route{routeTo("proxy", proxy)}, URLs: []string{pkgURL}}}, a, dir, fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
	noLeftovers(t, dir)
}

// 两条路径都给篡改内容：报 ErrChecksum，不留文件。
func TestDownloadTamperedEverywhere(t *testing.T) {
	payload, a := randomPayload(t, 128<<10)
	def := newSlowServer(t, payload, 8<<20)
	def.tamper = true
	proxy := newSlowServer(t, payload, 8<<20)
	proxy.tamper = true
	dir := t.TempDir()
	_, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", def), Alt: []Route{routeTo("proxy", proxy)}, URLs: []string{pkgURL}}}, a, dir, fastPolicy, nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// 两条路径都慢：到整体时长上限就放弃，不留文件。
func TestDownloadTotalDeadline(t *testing.T) {
	payload, a := randomPayload(t, 1<<20)
	def := newSlowServer(t, payload, 16<<10)
	proxy := newSlowServer(t, payload, 16<<10)
	pol := fastPolicy
	pol.Total = 1500 * time.Millisecond
	dir := t.TempDir()
	start := time.Now()
	_, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", def), Alt: []Route{routeTo("proxy", proxy)}, URLs: []string{pkgURL}}}, a, dir, pol, nil)
	if err == nil || !strings.Contains(err.Error(), "仍未完成") {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("deadline not honored: %s", time.Since(start))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// 端到端：检查走默认网络（很快，清单小），下载安装包时默认网络很慢，改走代理；状态里能看到速度和当前路径。
func TestE2ESlowDefaultDownloadSwitchesToProxy(t *testing.T) {
	k := newTestKey(t)
	payload, _ := randomPayload(t, 768<<10)
	version := "v0.5.0"
	r, _ := newFakeRelease(t, k, version, payload)
	// 同一个假 Release，经两台限速服务器提供安装包：默认网络 64KiB/s，代理 4MiB/s；清单和签名不限速。
	mk := func(rate int) (*httptest.Server, *int64) {
		var served int64
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, ".exe") {
				http.ServeContent(&throttled{w: w, rate: rate, ctx: req.Context(), count: &served, mu: &mu}, req, "pkg", time.Time{}, bytes.NewReader(payload))
				return
			}
			r.serve(w, req)
		}))
		t.Cleanup(srv.Close)
		return srv, &served
	}
	slowSrv, slowServed := mk(64 << 10)
	fastSrv, fastServed := mk(4 << 20)
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(slowSrv))
	u.opt.Download = fastPolicy
	u.opt.Preflight = func(ctx context.Context, path, want string) error { return nil } // 安装包是随机字节，不是版本号
	target := strings.TrimPrefix(fastSrv.URL, "http://")
	u.opt.Proxy = func() (DialFunc, bool) {
		return func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}, true
	}
	if st := u.Check(context.Background(), false); st.State != StateAvailable || st.Route != "default" {
		t.Fatalf("%+v", st)
	}
	var mu sync.Mutex
	routes := map[string]bool{}
	var maxSpeed int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(30 * time.Millisecond):
			}
			st := u.Status()
			mu.Lock()
			if st.DLRoute != "" {
				routes[st.DLRoute] = true
			}
			maxSpeed = max(maxSpeed, st.Speed)
			mu.Unlock()
		}
	}()
	start := time.Now()
	err := u.Apply(context.Background())
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatalf("took %s (default alone ≈12s)", time.Since(start))
	}
	if e.exeContent() != string(payload) || len(e.handover) != 1 || len(e.leftovers()) != 0 {
		t.Fatalf("exe swapped=%v handover=%v leftovers=%v", e.exeContent() == string(payload), e.handover, len(e.leftovers()))
	}
	mu.Lock()
	defer mu.Unlock()
	if !routes["default"] || !routes["proxy"] || maxSpeed == 0 {
		t.Fatalf("status routes %v speed %d", routes, maxSpeed)
	}
	t.Logf("default served %d bytes, proxy %d bytes, %s", *slowServed, *fastServed, time.Since(start).Round(10*time.Millisecond))
}

// 只有一条路径、卡住：判定卡住后在同一条路径上从断点续传。
func TestDownloadStallSingleRouteResumes(t *testing.T) {
	payload, a := randomPayload(t, 200<<10)
	s := newSlowServer(t, payload, 8<<20)
	s.stallAt = 64 << 10
	start := time.Now()
	path, _, _, err := DownloadMirrors(context.Background(), []Mirror{{Name: "github", Route: routeTo("default", s), URLs: []string{pkgURL}}}, a, t.TempDir(), fastPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, path, payload)
	if r := s.ranges(); len(r) != 2 || r[1] != "bytes=65536-" {
		t.Fatalf("requests %v", r)
	}
	if d := time.Since(start); d < fastPolicy.Stall || d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}
