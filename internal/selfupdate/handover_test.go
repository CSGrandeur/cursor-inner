package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// server 模拟一个进程里的本机代理：在固定地址上监听，回答自己的名字。
type server struct {
	name string
	addr string
	mu   sync.Mutex
	ln   net.Listener
}

func (s *server) listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.addr = ln.Addr().String()
	s.mu.Unlock()
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, s.name) }))
	return nil
}

func (s *server) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln.Close()
}

func (s *server) Reclaim() error {
	return Rebind(context.Background(), s.listen)
}

func ask(addr string) (string, error) {
	c := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get("http://" + addr + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// askRetry 模拟客户端对连接被拒绝的重试（Cursor / Chromium 都会重试新连接）。
func askRetry(addr string) (string, error) {
	var err error
	for i := 0; i < 50; i++ {
		var got string
		if got, err = ask(addr); err == nil {
			return got, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", err
}

type successorBehaviour int

const (
	succeed successorBehaviour = iota
	neverReady
	failAfterRelease
	wrongVersion
)

func runHandover(t *testing.T, behave successorBehaviour) (old *server, newSrv *server, ack Ack, err error, killed *int) {
	t.Helper()
	data := t.TempDir()
	old = &server{name: "old", addr: "127.0.0.1:0"}
	if err := old.listen(); err != nil {
		t.Fatal(err)
	}
	newSrv = &server{name: "new"}
	killed = new(int)
	var stopNew func()
	c := &Coordinator{
		Dir: HandoverDir(data),
		Spawn: func(plan Plan) error {
			go func() {
				s, err := JoinHandover(HandoverDir(data).PlanPath())
				if err != nil || behave == neverReady {
					return
				}
				s.Ready(4242)
				if err := s.WaitReleased(context.Background()); err != nil {
					return
				}
				if behave == failAfterRelease {
					return // 崩溃：不监听、不写回执
				}
				newSrv.addr = s.Plan.MitmAddr
				if err := Rebind(context.Background(), newSrv.listen); err != nil {
					return
				}
				stopNew = func() { newSrv.Release() }
				v := s.Plan.ToVersion
				if behave == wrongVersion {
					v = "v0.0.1"
				}
				s.Done(Ack{PID: 4243, Version: v, WebURL: "http://" + newSrv.addr})
			}()
			return nil
		},
		Kill: func(pid int) error {
			*killed = pid
			if stopNew != nil {
				stopNew()
			}
			return nil
		},
		Res: old,
		Health: func(ctx context.Context, a Ack) error {
			got, err := askRetry(old.addr)
			if err != nil {
				return err
			}
			if got != "new" {
				return fmt.Errorf("served by %s", got)
			}
			return nil
		},
		ReadyTimeout: 500 * time.Millisecond,
		OKTimeout:    500 * time.Millisecond,
	}
	ack, err = c.Run(context.Background(), Plan{Token: "t1", FromPID: os.Getpid(), FromVersion: "v0.3.0", ToVersion: "v0.4.0", MitmAddr: old.addr, TakeoverActive: true})
	return
}

func TestHandoverSameAddress(t *testing.T) {
	old, _, ack, err, _ := runHandover(t, succeed)
	if err != nil {
		t.Fatal(err)
	}
	if ack.PID != 4243 {
		t.Fatalf("ack %+v", ack)
	}
	got, err := askRetry(old.addr)
	if err != nil || got != "new" {
		t.Fatalf("after handover %q %v", got, err)
	}
}

func TestHandoverNeverReadyKeepsOldServing(t *testing.T) {
	old, _, _, err, _ := runHandover(t, neverReady)
	if err == nil || errors.Is(err, ErrRolledBack) {
		t.Fatalf("want pre-release failure, got %v", err)
	}
	if got, err := ask(old.addr); err != nil || got != "old" {
		t.Fatalf("old not serving: %q %v", got, err)
	}
}

func TestHandoverRollbackWhenSuccessorDies(t *testing.T) {
	old, _, _, err, killed := runHandover(t, failAfterRelease)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("want rollback, got %v", err)
	}
	if *killed != 4242 {
		t.Fatalf("successor not killed: %d", *killed)
	}
	if got, err := askRetry(old.addr); err != nil || got != "old" {
		t.Fatalf("old not reclaimed: %q %v", got, err)
	}
}

func TestHandoverRollbackOnBadAck(t *testing.T) {
	old, _, _, err, killed := runHandover(t, wrongVersion)
	if !errors.Is(err, ErrRolledBack) || *killed != 4243 {
		t.Fatalf("err=%v killed=%d", err, *killed)
	}
	if got, err := askRetry(old.addr); err != nil || got != "old" {
		t.Fatalf("old not reclaimed: %q %v", got, err)
	}
}

func TestHandedOver(t *testing.T) {
	data := t.TempDir()
	if HandedOver(data, 1) {
		t.Fatal("no record")
	}
	d := HandoverDir(data)
	os.MkdirAll(string(d), 0o700)
	writeJSON(filepath.Join(string(d), "succession.json"), Succession{FromPID: 77, ToPID: os.Getpid()})
	if !HandedOver(data, 77) {
		t.Fatal("live successor not recognised")
	}
	if HandedOver(data, 78) {
		t.Fatal("wrong pid matched")
	}
	writeJSON(filepath.Join(string(d), "succession.json"), Succession{FromPID: 77, ToPID: 999999})
	if HandedOver(data, 77) {
		t.Fatal("dead successor must not suppress restore")
	}
}

// Apply 交接失败时：程序文件换回旧版本，状态回到可更新。
func TestApplyRollbackRestoresExe(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(goodManifest)) })
	mux.HandleFunc("/cursor-inner-v0.4.0-windows-amd64.exe", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("abc")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	var exeDuringHandover string
	u := New(Options{
		Version: "v0.3.0", Exe: exe, DataDir: dir, Sources: []string{srv.URL + "/manifest.json"}, GOOS: "windows", GOARCH: "amd64",
		Preflight: func(ctx context.Context, path, want string) error { return nil },
		Handover: func(ctx context.Context, to string) error {
			b, _ := os.ReadFile(exe)
			exeDuringHandover = string(b)
			return ErrRolledBack
		},
	})
	if st := u.Check(context.Background(), false); st.State != StateAvailable {
		t.Fatalf("%+v", st)
	}
	if err := u.Apply(context.Background()); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("err=%v", err)
	}
	if exeDuringHandover != "abc" {
		t.Fatalf("new exe not in place during handover: %q", exeDuringHandover)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("exe not restored: %q", b)
	}
	if st := u.Status(); st.State != StateAvailable || st.Error == "" {
		t.Fatalf("status %+v", st)
	}
}

func TestApplyChecksumFailureNeverSwaps(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(goodManifest)) })
	mux.HandleFunc("/cursor-inner-v0.4.0-windows-amd64.exe", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("xyz")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	called := false
	u := New(Options{Version: "v0.3.0", Exe: exe, DataDir: dir, Sources: []string{srv.URL + "/manifest.json"}, GOOS: "windows", GOARCH: "amd64",
		Preflight: func(context.Context, string, string) error { return nil },
		Handover:  func(context.Context, string) error { called = true; return nil }})
	u.Check(context.Background(), false)
	if err := u.Apply(context.Background()); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err=%v", err)
	}
	if called {
		t.Fatal("handover ran after checksum failure")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("exe touched: %q", b)
	}
}

func TestWaitIdleDefers(t *testing.T) {
	busy := true
	var mu sync.Mutex
	go func() { time.Sleep(100 * time.Millisecond); mu.Lock(); busy = false; mu.Unlock() }()
	start := time.Now()
	err := WaitIdle(context.Background(), func() bool { mu.Lock(); defer mu.Unlock(); return busy }, 50*time.Millisecond, 10*time.Millisecond)
	if err != nil || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := WaitIdle(ctx, func() bool { return true }, 10*time.Millisecond, 5*time.Millisecond); err == nil {
		t.Fatal("expected deferral error")
	}
}

func TestHandoverDetectsEarlySuccessorDeath(t *testing.T) {
	data := t.TempDir()
	old := &server{name: "old", addr: "127.0.0.1:0"}
	if err := old.listen(); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{
		Dir: HandoverDir(data),
		Spawn: func(plan Plan) error {
			go func() {
				s, _ := JoinHandover(HandoverDir(data).PlanPath())
				s.Ready(4242)
			}()
			return nil
		},
		Kill:         func(int) error { return nil },
		Alive:        func(int) bool { return false },
		Res:          old,
		Health:       func(context.Context, Ack) error { return nil },
		ReadyTimeout: time.Second,
		OKTimeout:    10 * time.Second,
	}
	start := time.Now()
	_, err := c.Run(context.Background(), Plan{Token: "t", FromPID: 1, ToVersion: "v1.0.0", MitmAddr: old.addr})
	if !errors.Is(err, ErrRolledBack) || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	if got, err := askRetry(old.addr); err != nil || got != "old" {
		t.Fatalf("old not reclaimed: %q %v", got, err)
	}
}

func TestSuccessorWaitConfirmed(t *testing.T) {
	data := t.TempDir()
	d := HandoverDir(data)
	os.MkdirAll(string(d), 0o700)
	s := &Successor{Dir: d, Plan: Plan{FromPID: 10}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.WaitConfirmed(ctx, 20); err == nil {
		t.Fatal("confirmed without record")
	}
	writeJSON(filepath.Join(string(d), "succession.json"), Succession{FromPID: 10, ToPID: 20})
	if err := s.WaitConfirmed(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
}
