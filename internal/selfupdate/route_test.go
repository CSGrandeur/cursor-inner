package selfupdate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func brokenRoute(name string) Route {
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("blocked")
	}}
	return Route{Name: name, Client: &http.Client{Transport: tr}}
}

func hangingRoute(name string) Route {
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	return Route{Name: name, Client: &http.Client{Transport: tr}}
}

func okRoute(name string) Route { return Route{Name: name, Client: &http.Client{}} }

func TestFetchFirstPrefersDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer srv.Close()
	got, attempts, err := FetchFirst(context.Background(), []Route{okRoute("default"), okRoute("proxy")}, srv.URL, time.Second, 100)
	if err != nil || got.Route.Name != "default" || len(attempts) != 1 {
		t.Fatalf("route=%s attempts=%v err=%v", got.Route.Name, attempts, err)
	}
}

func TestFetchFirstFallsBackOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer srv.Close()
	got, attempts, err := FetchFirst(context.Background(), []Route{brokenRoute("default"), okRoute("proxy")}, srv.URL, time.Second, 100)
	if err != nil || got.Route.Name != "proxy" || string(got.Body) != "hi" {
		t.Fatalf("route=%s err=%v", got.Route.Name, err)
	}
	if len(attempts) != 2 || attempts[0].Err == nil || attempts[1].Err != nil {
		t.Fatalf("attempts %v", attempts)
	}
}

func TestFetchFirstFallsBackOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer srv.Close()
	start := time.Now()
	got, _, err := FetchFirst(context.Background(), []Route{hangingRoute("default"), okRoute("proxy")}, srv.URL, 200*time.Millisecond, 100)
	if err != nil || got.Route.Name != "proxy" {
		t.Fatalf("route=%s err=%v", got.Route.Name, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout not applied")
	}
}

func TestFetchFirstAllFail(t *testing.T) {
	_, attempts, err := FetchFirst(context.Background(), []Route{brokenRoute("default"), brokenRoute("proxy")}, "http://127.0.0.1:1/x", time.Second, 100)
	if err == nil || len(attempts) != 2 {
		t.Fatalf("err=%v attempts=%v", err, attempts)
	}
	_, attempts, err = FetchFirst(context.Background(), []Route{brokenRoute("default")}, "http://127.0.0.1:1/x", time.Second, 100)
	if err == nil || len(attempts) != 1 {
		t.Fatalf("no proxy: err=%v attempts=%v", err, attempts)
	}
}

// 检查选定的路径就是下载用的路径：默认路径坏、代理路径好时，下载也走代理路径。
func TestUpdaterUsesCheckedRouteForDownload(t *testing.T) {
	payload := []byte("abc")
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(goodManifest)) })
	hits := 0
	mux.HandleFunc("/cursor-inner-v0.4.0-windows-amd64.exe", func(w http.ResponseWriter, r *http.Request) { hits++; w.Write(payload) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := New(Options{Version: "v0.3.0", DataDir: t.TempDir(), Sources: []string{srv.URL + "/manifest.json"}, GOOS: "windows", GOARCH: "amd64", Timeout: time.Second})
	proxyUsed := 0
	u.opt.Proxy = func() (DialFunc, bool) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			proxyUsed++
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}, true
	}
	routes := u.Routes()
	if len(routes) != 2 || routes[0].Name != "default" || routes[1].Name != "proxy" {
		t.Fatalf("routes %v", routes)
	}
	// 让默认路径失败。
	defaultBroken := brokenRoute("default")
	got, _, err := FetchFirst(context.Background(), []Route{defaultBroken, routes[1]}, srv.URL+"/manifest.json", time.Second, 1<<20)
	if err != nil || got.Route.Name != "proxy" {
		t.Fatalf("%v %v", got.Route.Name, err)
	}
	m, _, _ := ParseManifest(got.Body, nil, nil)
	a, _ := m.Pick("windows", "amd64")
	before := proxyUsed
	path, err := Download(context.Background(), got.Route, assetURLs(a, got.FinalURL), a, t.TempDir(), nil)
	if err != nil || path == "" || hits != 1 {
		t.Fatalf("download err=%v hits=%d", err, hits)
	}
	if proxyUsed == before && before == 0 {
		t.Fatalf("download did not use the proxy route")
	}
}

func TestUpdaterCheckRecordsRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(goodManifest)) }))
	defer srv.Close()
	u := New(Options{Version: "v0.3.0", DataDir: t.TempDir(), Sources: []string{srv.URL + "/manifest.json"}, GOOS: "windows", GOARCH: "amd64", Timeout: time.Second})
	st := u.Check(context.Background(), true)
	if st.State != StateAvailable || st.Route != "default" || st.Latest != "v0.4.0" || st.Notes == "" {
		t.Fatalf("%+v", st)
	}
	// 启动自动检查只做一次。
	srv.Close()
	if st2 := u.Check(context.Background(), true); st2.State != StateAvailable {
		t.Fatalf("auto check ran twice: %+v", st2)
	}
	if st3 := u.Check(context.Background(), false); st3.State != StateFailed {
		t.Fatalf("manual check should run again: %+v", st3)
	}
}
