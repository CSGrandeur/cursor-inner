package program

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/app"
	"cursor-inner/internal/config"
	"cursor-inner/internal/console"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/selfupdate"
	"cursor-inner/internal/takeover"
)

// relisten 让配置页的监听可以交出、收回（地址不变，浏览器里的页面刷新即可）。
type relisten struct {
	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
	addr string
}

func (r *relisten) Release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return nil
	}
	r.addr = r.ln.Addr().String()
	err := r.ln.Close()
	r.ln = nil
	return err
}

func (r *relisten) Reclaim() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln != nil || r.addr == "" {
		return nil
	}
	ln, err := listenWeb(r.addr)
	if err != nil {
		return err
	}
	r.ln = ln
	go func() { _ = r.srv.Serve(ln) }()
	return nil
}

// listenWeb 在指定地址监听；交接时对方可能还没释放，短暂重试。随机端口直接监听。
func listenWeb(addr string) (net.Listener, error) {
	var ln net.Listener
	err := selfupdate.Rebind(context.Background(), func() error {
		l, err := net.Listen("tcp", addr)
		if err == nil {
			ln = l
		}
		return err
	})
	if err != nil && !strings.HasSuffix(addr, ":0") {
		// 配置页端口被别人占了不影响代理，退回随机端口；listen.url 会更新。
		return net.Listen("tcp", "127.0.0.1:0")
	}
	return ln, err
}

// acquireSingletonRetry 在交接时等旧进程释放单实例。
func acquireSingletonRetry(dir string, wait bool) (func(), bool, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, already, err := acquireSingleton(dir)
		if err != nil || !already || !wait || time.Now().After(deadline) {
			return release, already, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// singletonRes 交出、收回单实例锁。
type singletonRes struct {
	dir     string
	release *func()
}

func (s singletonRes) Release() error {
	if s.release != nil && *s.release != nil {
		(*s.release)()
		*s.release = func() {}
	}
	return nil
}

func (s singletonRes) Reclaim() error {
	rel, already, err := acquireSingletonRetry(s.dir, true)
	if err != nil {
		return err
	}
	if already {
		return errors.New("单实例锁仍被占用")
	}
	*s.release = rel
	return nil
}

type funcRes struct{ release, reclaim func() error }

func (f funcRes) Release() error { return f.release() }
func (f funcRes) Reclaim() error { return f.reclaim() }

// multiRes 依次交出；收回时全部都试（倒序），返回第一个错误。
type multiRes []selfupdate.Releaser

func (m multiRes) Release() error {
	for i, r := range m {
		if err := r.Release(); err != nil {
			for j := i - 1; j >= 0; j-- {
				_ = m[j].Reclaim()
			}
			return err
		}
	}
	return nil
}

func (m multiRes) Reclaim() error {
	var first error
	for i := len(m) - 1; i >= 0; i-- {
		if err := m[i].Reclaim(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type updateGlue struct {
	version string
	dir     string
	exe     string
	life    *takeover.Service
	proxy   *mitm.Server
	app     *app.App
	web     *relisten
	release *func()
	term    *console.Console
	updater *selfupdate.Updater
}

func newUpdateGlue(version, dir string, debug bool, store *config.Store, life *takeover.Service, proxy *mitm.Server, application *app.App, web *relisten, release *func(), term *console.Console) *updateGlue {
	g := &updateGlue{version: version, dir: dir, exe: executable(), life: life, proxy: proxy, app: application, web: web, release: release, term: term}
	key, err := selfupdate.EmbeddedKey()
	if err != nil {
		slog.Warn(err.Error())
	}
	g.updater = selfupdate.New(selfupdate.Options{
		Version:  version,
		Exe:      g.exe, // 启动时记下路径：交接时正在运行的文件会被改名
		DataDir:  dir,
		Proxy:    application.UpdateProxy,
		Busy:     proxy.LocalBusy,
		Handover: g.handover,
		Key:      key,
		Sources:  debugSources(debug),
	})
	_ = store
	return g
}

func (g *updateGlue) handover(ctx context.Context, to string) error {
	snap := g.life.Snapshot()
	plan := selfupdate.Plan{
		Token:          randomToken(),
		FromPID:        os.Getpid(),
		FromVersion:    g.version,
		ToVersion:      to,
		TakeoverActive: snap.Active,
	}
	if u, err := url.Parse(snap.URL); err == nil && snap.Active {
		plan.MitmAddr = u.Host
	}
	if g.web.ln != nil {
		plan.WebAddr = g.web.ln.Addr().String()
	}
	g.app.Freeze(true)
	res := multiRes{
		funcRes{g.proxy.Release, g.proxy.Reclaim},
		funcRes{g.app.ReleaseBridge, g.app.ReclaimBridge},
		g.web,
		singletonRes{dir: g.dir, release: g.release},
	}
	c := &selfupdate.Coordinator{
		Dir: selfupdate.HandoverDir(g.dir),
		Spawn: func(p selfupdate.Plan) error {
			args := append(passthroughArgs(os.Args[1:]), "--handover", selfupdate.HandoverDir(g.dir).PlanPath(), "--data-dir", g.dir)
			return spawnDetached(g.exe, args...)
		},
		Kill:         selfupdate.KillProcess,
		Alive:        selfupdate.ProcessAlive,
		Res:          res,
		Health:       func(ctx context.Context, ack selfupdate.Ack) error { return checkSuccessor(ctx, ack, plan) },
		ReadyTimeout: 30 * time.Second,
		OKTimeout:    30 * time.Second,
	}
	log.Printf("开始交接到 %s", to)
	ack, err := c.Run(ctx, plan)
	if err != nil {
		g.app.Freeze(false)
		slog.Error("交接失败：" + err.Error())
		return err
	}
	log.Printf("已交接给 %s（pid %d），本进程排空连接后退出", ack.Version, ack.PID)
	go g.drainAndExit()
	return nil
}

// checkSuccessor 是旧进程对新进程的外部健康检查：配置页报对版本号，接管时本机代理端口能连上。
func checkSuccessor(ctx context.Context, ack selfupdate.Ack, plan selfupdate.Plan) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ack.WebURL+"/api/version", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("新版本配置页无响应：%w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if !strings.Contains(string(body), `"`+plan.ToVersion+`"`) {
		return fmt.Errorf("新版本报告的版本不对：%s", body)
	}
	if plan.TakeoverActive && plan.MitmAddr != "" {
		c, err := net.DialTimeout("tcp", plan.MitmAddr, 3*time.Second)
		if err != nil {
			return fmt.Errorf("新版本没有接上本机代理端口：%w", err)
		}
		c.Close()
	}
	return nil
}

// drainAndExit：新连接已经全部去了新进程，等本进程里的旧连接（流式回合、隧道）结束再退出。
// 不调用 Shutdown：接管已经交给新进程，不能还原 Cursor 设置。
func (g *updateGlue) drainAndExit() {
	removeTrayIcon()
	deadline := time.Now().Add(10 * time.Minute)
	for g.proxy.ActiveConns() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	g.term.Close()
	os.Exit(0)
}

// cleanupOld 删掉上次更新留下的旧程序；交接刚完成时旧进程还占着文件，稍后重试。
func (g *updateGlue) cleanupOld(justHandedOver bool) {
	selfupdate.CleanupOld(g.exe)
	if !justHandedOver {
		return
	}
	go func() {
		for i := 0; i < 40; i++ {
			time.Sleep(30 * time.Second)
			selfupdate.CleanupOld(g.exe)
		}
	}()
}

// trayCheck 是托盘菜单「检查更新」：有新版本时弹确认框。
func (g *updateGlue) trayCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	st := g.updater.Check(ctx, false)
	cancel()
	zh := uiChinese()
	switch st.State {
	case selfupdate.StateAvailable:
		notes := st.Notes
		if len([]rune(notes)) > 600 {
			notes = string([]rune(notes)[:600]) + "…"
		}
		q := fmt.Sprintf("Update cursor-inner %s → %s?\n\n%s\n\nGrok and Cursor keep running during the update.", st.Current, st.Latest, notes)
		if zh {
			q = fmt.Sprintf("要把 cursor-inner 从 %s 更新到 %s 吗？\n\n%s\n\n更新期间 Grok 和 Cursor 不需要重启。", st.Current, st.Latest, notes)
		}
		if !confirmDialog(q) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		if err := g.updater.Apply(ctx); err != nil {
			msg := "Update failed; still running the current version.\n\n" + err.Error()
			if zh {
				msg = "更新没有完成，仍在运行当前版本。\n\n" + err.Error()
			}
			infoDialog(msg)
		}
	case selfupdate.StateUpToDate:
		msg := "cursor-inner " + st.Current + " is up to date."
		if zh {
			msg = "cursor-inner " + st.Current + " 已是最新版本。"
		}
		infoDialog(msg)
	default:
		msg := "Could not check for updates.\n\n" + st.Error
		if zh {
			msg = "检查更新失败。\n\n" + st.Error
		}
		infoDialog(msg)
	}
}

// debugSources：只有 --debug 调试实例才认 CURSOR_INNER_UPDATE_MANIFEST，用来对本机假 Release 做交接演练。
func debugSources(debug bool) []string {
	if src := os.Getenv("CURSOR_INNER_UPDATE_MANIFEST"); debug && src != "" {
		return []string{src}
	}
	return nil
}

// passthroughArgs 把影响行为的启动参数原样带给新进程（--debug、--no-takeover、--verbose），
// 去掉会重复或只属于本进程的参数。
func passthroughArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--debug", "--no-takeover", "--verbose":
			out = append(out, args[i])
		case "--data-dir", "--handover", "--watch":
			i++
		}
	}
	return out
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
