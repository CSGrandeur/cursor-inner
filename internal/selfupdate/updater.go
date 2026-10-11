package selfupdate

import (
	"context"
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
	State     string         `json:"state"`
	Current   string         `json:"current"`
	Latest    string         `json:"latest,omitempty"`
	Notes     string         `json:"notes,omitempty"`
	Published string         `json:"published,omitempty"`
	Signed    bool           `json:"signed"`
	KeyID     string         `json:"key_id,omitempty"`
	Source    string         `json:"source,omitempty"` // 选中的更新源
	Route     string         `json:"route,omitempty"`
	Sources   []SourceStatus `json:"sources,omitempty"`
	// CanApply 为假时（没有内置公钥的构建）只提示有新版本，不提供「更新」。
	CanApply  bool      `json:"can_apply"`
	Mirror    string    `json:"mirror,omitempty"` // 实际下载安装包的源
	Error     string    `json:"error,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
	Done      int64     `json:"done,omitempty"`
	Total     int64     `json:"total,omitempty"`
	Speed     int64     `json:"speed,omitempty"`    // 下载速度，字节/秒（滑动窗口）
	DLRoute   string    `json:"dl_route,omitempty"` // 下载安装包当前走的网络路径（可能和检查时不同）
	AutoDone  bool      `json:"auto_done"`
}

// Options 由 program 包注入，本包不认识接管、Grok 或 Cursor。
type Options struct {
	Version  string
	Exe      string
	DataDir  string
	Sources  []Source
	Proxy    func() (DialFunc, bool) // 配置了出站代理时返回拨号函数
	Busy     func() bool             // 有自定义模型回合在跑
	Handover func(ctx context.Context, toVersion string) error
	Timeout  time.Duration // 每条路径检查超时
	// Keys 是可信的清单签名公钥。为空表示没有内置公钥：可以提示新版本，但拒绝安装（fail closed）。
	Keys   []TrustedKey
	Now    func() time.Time
	GOOS   string
	GOARCH string
	// Download 是下载的测速、换路径、续传和时长上限策略；零值用默认值。
	Download DownloadPolicy
	// Preflight 默认跑新版本的 --version；测试可替换。
	Preflight func(ctx context.Context, exe, want string) error
}

// Updater 串起检查、下载、预检和交接。
type Updater struct {
	opt Options

	mu      sync.Mutex
	st      Status
	results []sourceResult
	best    int
	running bool
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
	return &Updater{opt: opt, best: -1, st: Status{State: StateIdle, Current: opt.Version, CanApply: len(opt.Keys) > 0}}
}

func (u *Updater) now() time.Time {
	if u.opt.Now != nil {
		return u.opt.Now()
	}
	return time.Now()
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

	results := u.fetchAll(ctx)
	now := u.now()
	best := pickBest(results)
	statuses := make([]SourceStatus, len(results))
	for i, r := range results {
		statuses[i] = SourceStatus{Name: r.src.Name, Route: r.route.Name, Millis: r.elapsed.Milliseconds(), Chosen: i == best}
		if r.err != nil {
			statuses[i].Error, statuses[i].Code = r.err.Error(), ErrorCode(r.err)
			slog.Info("更新源不可用", "source", r.src.Name, "err", r.err.Error())
		} else {
			statuses[i].Version = r.manifest.Version
		}
	}
	var err error
	if best < 0 {
		err = joinSourceErrors(results)
	} else {
		err = checkRollback(u.opt.DataDir, u.opt.Version, results[best].manifest.Version)
	}
	if err != nil {
		code := ErrorCode(err)
		if code == "" {
			code = CodeNetwork
		}
		slog.Warn("检查更新失败：" + err.Error())
		u.set(func(s *Status) {
			s.State, s.Error, s.ErrorCode, s.CheckedAt, s.Sources = StateFailed, err.Error(), code, now, statuses
			s.Latest, s.Notes, s.Published, s.Signed, s.KeyID, s.Source, s.Route, s.Mirror = "", "", "", false, "", "", "", ""
		})
		return u.Status()
	}
	r := results[best]
	m := r.manifest
	noteSeen(u.opt.DataDir, m.Version, now)
	slog.Info("检查更新", "latest", m.Version, "source", r.src.Name, "route", r.route.Name, "key", r.keyID)
	u.mu.Lock()
	u.results, u.best = results, best
	u.st.Mirror = ""
	u.st.Latest, u.st.Notes, u.st.Published, u.st.CheckedAt = m.Version, m.Notes, m.Published, now
	u.st.Signed, u.st.KeyID, u.st.Source, u.st.Route, u.st.Sources = r.keyID != "", r.keyID, r.src.Name, r.route.Name, statuses
	u.st.Error, u.st.ErrorCode, u.st.CanApply = "", "", len(u.opt.Keys) > 0
	allowPre := strings.Contains(u.opt.Version, "-")
	if Newer(u.opt.Version, m.Version, allowPre) {
		if _, perr := m.Pick(u.opt.GOOS, u.opt.GOARCH); perr != nil {
			u.st.State, u.st.Error = StateFailed, perr.Error()
		} else {
			u.st.State = StateAvailable
			if len(u.opt.Keys) == 0 {
				u.st.Error, u.st.ErrorCode = ErrNoKey.Error(), CodeNoKey
			}
		}
	} else {
		u.st.State = StateUpToDate
	}
	st := u.st
	u.mu.Unlock()
	return st
}

// fetchAll 并行查询所有源（每个源内部仍是默认路径优先、失败再走代理）。
func (u *Updater) fetchAll(ctx context.Context) []sourceResult {
	routes := u.Routes()
	results := make([]sourceResult, len(u.opt.Sources))
	var wg sync.WaitGroup
	for i, src := range u.opt.Sources {
		wg.Add(1)
		go func(i int, src Source) {
			defer wg.Done()
			results[i] = u.fetchSource(ctx, src, routes)
		}(i, src)
	}
	wg.Wait()
	return results
}

// joinSourceErrors 合并各源的错误。有安全相关的错误（验签失败等）时把它放在最前面，
// 这样 ErrorCode 归到它那一类：签名有问题比网络不通更需要用户知道。
func joinSourceErrors(results []sourceResult) error {
	var trust, other []error
	for _, r := range results {
		if r.err == nil {
			continue
		}
		e := fmt.Errorf("%s：%w", r.src.Name, r.err)
		switch ErrorCode(r.err) {
		case CodeNoSignature, CodeBadSignature, CodeUnknownKey, CodeExpired, CodeBadManifest:
			trust = append(trust, e)
		default:
			other = append(other, e)
		}
	}
	all := append(trust, other...)
	if len(all) == 0 {
		return errors.New("没有配置更新源")
	}
	if len(all) == 1 {
		return all[0]
	}
	return errors.Join(all...)
}

// Apply 下载、校验、预检新版本，等空闲后交接。阻塞到交接结束或失败。
func (u *Updater) Apply(ctx context.Context) error {
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return errors.New("更新正在进行")
	}
	if u.st.State != StateAvailable || u.best < 0 {
		u.mu.Unlock()
		return errors.New("没有可用的更新，先检查更新")
	}
	if len(u.opt.Keys) == 0 {
		u.mu.Unlock()
		return ErrNoKey
	}
	results, best := u.results, u.best
	u.running = true
	u.st.State, u.st.Error = StateDownloading, ""
	u.st.Done, u.st.Total, u.st.Speed, u.st.DLRoute = 0, 0, 0, ""
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.running = false; u.mu.Unlock() }()

	err := u.apply(ctx, results, best)
	if err != nil {
		slog.Error("更新失败：" + err.Error())
		u.set(func(s *Status) { s.State, s.Error, s.ErrorCode = StateFailed, err.Error(), ErrorCode(err) })
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

func (u *Updater) apply(ctx context.Context, results []sourceResult, best int) error {
	m := results[best].manifest
	a, err := m.Pick(u.opt.GOOS, u.opt.GOARCH)
	if err != nil {
		return err
	}
	dir := filepath.Join(u.opt.DataDir, "update")
	staged, used, fails, err := DownloadMirrors(ctx, mirrorsFor(results, best, a, u.Routes()), a, dir, u.opt.Download, func(p Progress) {
		u.set(func(s *Status) { s.Done, s.Total, s.Speed, s.DLRoute = p.Done, p.Total, p.Rate, p.Route })
	})
	for _, f := range fails {
		slog.Warn("更新包下载失败，换下一个源", "source", f.Mirror, "err", f.Err.Error())
	}
	if err != nil {
		return err
	}
	slog.Info("更新包已下载并校验", "source", used, "sha256", a.SHA256)
	u.set(func(s *Status) { s.Mirror = used })
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
