package selfupdate

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultSources 是清单地址，按顺序尝试。GitHub 的 latest/download 跳转不走 API，不受匿名限流影响。
// 签名清单里的 sha256 才是信任根，所以以后加镜像（如 Gitee）不会降低安全性。
var DefaultSources = []string{
	"https://github.com/CSGrandeur/cursor-inner/releases/latest/download/manifest.json",
}

// 状态机。
const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateUpToDate    = "up_to_date"
	StateAvailable   = "available"
	StateDownloading = "downloading"
	StateWaitingIdle = "waiting_idle" // 有自定义模型回合在跑，等它结束
	StateHandover    = "handover"
	StateDone        = "done" // 已交给新进程，本进程正在排空
	StateFailed      = "failed"
)

// Status 给配置页和托盘看。
type Status struct {
	State     string    `json:"state"`
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	Published string    `json:"published,omitempty"`
	Signed    bool      `json:"signed"`
	Route     string    `json:"route,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
	Done      int64     `json:"done,omitempty"`
	Total     int64     `json:"total,omitempty"`
	AutoDone  bool      `json:"auto_done"`
}

// Options 由 program 包注入，本包不认识接管、Grok 或 Cursor。
type Options struct {
	Version  string
	Exe      string
	DataDir  string
	Sources  []string
	Proxy    func() (DialFunc, bool) // 配置了出站代理时返回拨号函数
	Busy     func() bool             // 有自定义模型回合在跑
	Handover func(ctx context.Context, toVersion string) error
	Timeout  time.Duration // 每条路径检查超时
	Key      ed25519.PublicKey
	GOOS     string
	GOARCH   string
	// Preflight 默认跑新版本的 --version；测试可替换。
	Preflight func(ctx context.Context, exe, want string) error
}

// Updater 串起检查、下载、预检和交接。
type Updater struct {
	opt Options

	mu       sync.Mutex
	st       Status
	manifest Manifest
	manURL   string
	route    Route
	running  bool
}

func New(opt Options) *Updater {
	if len(opt.Sources) == 0 {
		opt.Sources = DefaultSources
	}
	if opt.Timeout == 0 {
		opt.Timeout = 8 * time.Second
	}
	if opt.GOOS == "" {
		opt.GOOS, opt.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	return &Updater{opt: opt, st: Status{State: StateIdle, Current: opt.Version}}
}

func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *Updater) set(fn func(*Status)) {
	u.mu.Lock()
	fn(&u.st)
	u.mu.Unlock()
}

// Routes 按「默认路径优先，配置了代理再加代理路径」排好。
func (u *Updater) Routes() []Route {
	routes := []Route{DefaultRoute()}
	if u.opt.Proxy != nil {
		if d, ok := u.opt.Proxy(); ok && d != nil {
			routes = append(routes, ProxyRoute(d))
		}
	}
	return routes
}

// Check 取清单并比较版本。auto 为真表示启动时的自动检查，每个进程只做一次。
func (u *Updater) Check(ctx context.Context, auto bool) Status {
	u.mu.Lock()
	if auto && u.st.AutoDone {
		st := u.st
		u.mu.Unlock()
		return st
	}
	if u.running {
		st := u.st
		u.mu.Unlock()
		return st
	}
	if auto {
		u.st.AutoDone = true
	}
	u.running = true
	u.st.State, u.st.Error = StateChecking, ""
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.running = false; u.mu.Unlock() }()

	m, signed, url, route, err := u.fetchManifest(ctx)
	now := time.Now()
	if err != nil {
		slog.Warn("检查更新失败：" + err.Error())
		u.set(func(s *Status) { s.State, s.Error, s.CheckedAt = StateFailed, err.Error(), now })
		return u.Status()
	}
	slog.Info("检查更新", "latest", m.Version, "route", route.Name, "signed", signed)
	u.mu.Lock()
	u.manifest, u.manURL, u.route = m, url, route
	u.st.Latest, u.st.Notes, u.st.Published, u.st.Signed, u.st.Route, u.st.CheckedAt = m.Version, m.Notes, m.Published, signed, route.Name, now
	allowPre := strings.Contains(u.opt.Version, "-")
	if Newer(u.opt.Version, m.Version, allowPre) {
		if _, perr := m.Pick(u.opt.GOOS, u.opt.GOARCH); perr != nil {
			u.st.State, u.st.Error = StateFailed, perr.Error()
		} else {
			u.st.State = StateAvailable
		}
	} else {
		u.st.State = StateUpToDate
	}
	st := u.st
	u.mu.Unlock()
	return st
}

func (u *Updater) fetchManifest(ctx context.Context) (Manifest, bool, string, Route, error) {
	routes := u.Routes()
	var errs []error
	for _, src := range u.opt.Sources {
		got, attempts, err := FetchFirst(ctx, routes, src, u.opt.Timeout, 1<<20)
		for _, a := range attempts {
			if a.Err != nil {
				slog.Info("更新路径不可用", "route", a.Route, "err", a.Err.Error())
			}
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// 签名和清单必须走同一条路径。
		var sig []byte
		if len(u.opt.Key) > 0 {
			s, _, serr := fetch(ctx, got.Route, got.FinalURL+".sig", u.opt.Timeout, 4096)
			if serr != nil {
				errs = append(errs, fmt.Errorf("取签名失败：%w", serr))
				continue
			}
			sig = s
		}
		m, signed, perr := ParseManifest(got.Body, sig, u.opt.Key)
		if perr != nil {
			errs = append(errs, perr)
			continue
		}
		slog.Info("更新使用的网络路径", "route", got.Route.Name, "source", src)
		return m, signed, got.FinalURL, got.Route, nil
	}
	return Manifest{}, false, "", Route{}, errors.Join(errs...)
}

// Apply 下载、校验、预检新版本，等空闲后交接。阻塞到交接结束或失败。
func (u *Updater) Apply(ctx context.Context) error {
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return errors.New("更新正在进行")
	}
	if u.st.State != StateAvailable {
		u.mu.Unlock()
		return errors.New("没有可用的更新，先检查更新")
	}
	m, manURL, route := u.manifest, u.manURL, u.route
	u.running = true
	u.st.State, u.st.Error = StateDownloading, ""
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.running = false; u.mu.Unlock() }()

	err := u.apply(ctx, m, manURL, route)
	if err != nil {
		slog.Error("更新失败：" + err.Error())
		u.set(func(s *Status) { s.State, s.Error = StateFailed, err.Error() })
		// 失败后仍可以重试。
		u.set(func(s *Status) {
			if s.Latest != "" && Newer(u.opt.Version, s.Latest, true) {
				s.State = StateAvailable
			}
		})
		return err
	}
	u.set(func(s *Status) { s.State = StateDone })
	return nil
}

func (u *Updater) apply(ctx context.Context, m Manifest, manURL string, route Route) error {
	a, err := m.Pick(u.opt.GOOS, u.opt.GOARCH)
	if err != nil {
		return err
	}
	dir := filepath.Join(u.opt.DataDir, "update")
	staged, err := Download(ctx, route, assetURLs(a, manURL), a, dir, func(done, total int64) {
		u.set(func(s *Status) { s.Done, s.Total = done, total })
	})
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	pre := u.opt.Preflight
	if pre == nil {
		pre = Preflight
	}
	if err := pre(ctx, staged, m.Version); err != nil {
		return err
	}
	if u.opt.Busy != nil && u.opt.Busy() {
		u.set(func(s *Status) { s.State = StateWaitingIdle })
		if err := WaitIdle(ctx, u.opt.Busy, 3*time.Second, time.Second); err != nil {
			return err
		}
	}
	u.set(func(s *Status) { s.State = StateHandover })
	backup, err := Swap(u.opt.Exe, staged)
	if err != nil {
		return err
	}
	if err := u.opt.Handover(ctx, m.Version); err != nil {
		if rerr := Revert(u.opt.Exe, backup); rerr != nil {
			return fmt.Errorf("%v；还原程序文件失败：%v", err, rerr)
		}
		return err
	}
	return nil
}

// Preflight 在交接前跑一次新版本的 --version，确认它能在本机启动、版本号对得上。
// 不监听任何端口，不影响正在运行的实例。
func Preflight(ctx context.Context, exe, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").Output()
	if err != nil {
		return fmt.Errorf("新版本无法在本机启动：%w", err)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		return fmt.Errorf("新版本报告的版本号是 %q，清单写的是 %q", got, want)
	}
	return nil
}

// WaitIdle 等 busy 连续 quiet 时长都为假。ctx 结束则放弃（更新推迟，不强行打断回合）。
func WaitIdle(ctx context.Context, busy func() bool, quiet, poll time.Duration) error {
	var since time.Time
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if busy() {
			since = time.Time{}
		} else if since.IsZero() {
			since = time.Now()
		} else if time.Since(since) >= quiet {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("自定义模型回合一直在跑，更新已推迟：%w", ctx.Err())
		case <-t.C:
		}
	}
}
