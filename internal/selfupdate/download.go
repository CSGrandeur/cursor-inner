package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/logx"
)

// ErrChecksum 表示下载内容和清单里的 sha256 或大小对不上。文件已删掉，不会被使用。
var ErrChecksum = errors.New("下载内容校验失败")

// errStall 表示一条路径在 Stall 时长里一个字节都没给（包括迟迟不回响应头）。
var errStall = errors.New("下载卡住，长时间没有收到数据")

// Mirror 是一个可以下载安装包的地方：一个更新源加上它验过清单时用的网络路径。
// Alt 是同一个地址的其他网络路径（通常是代理）：主路径太慢、卡住或断开时并行试它们，或从断点改走它们。
type Mirror struct {
	Name  string
	Route Route
	Alt   []Route
	URLs  []string
}

// MirrorFailure 记录某个镜像为什么没用上，写进日志和界面。
type MirrorFailure struct {
	Mirror string
	Err    error
}

// Progress 是下载进度，给配置页和控制台看。Done 是从头开始已连续写好的字节数。
type Progress struct {
	Done, Total int64
	Rate        int64  // 整体进度（Done）在滑动窗口里的速度，字节/秒；还没测出来时为 0
	Route       string // 正在用的网络路径（并行比较时是原来那条）
	Probe       string // 正在并行试的另一条路径；没在比较时为空
	Verifying   bool   // 已下完，正在整份重读核对大小和 sha256
}

// DownloadPolicy 决定什么时候认为一条路径太慢、卡住，以及整体最多等多久。零值字段取默认值。
type DownloadPolicy struct {
	Warmup  time.Duration // 一条路径开始后先等这么久再测速（默认 4s，TCP 慢启动、TLS、重定向都在这段里）
	Window  time.Duration // 测速的滑动窗口（默认 8s）
	SlowETA time.Duration // 按窗口速度估算剩余时间超过它就算慢，并行试下一条路径（默认 45s）
	Race    time.Duration // 两条路径并行比较多久（默认 6s）
	Stall   time.Duration // 这么久一个字节都没收到算卡住（默认 20s）
	Total   time.Duration // 一个地址的整体时长上限（默认 15min）
	Retries int           // 断开后从断点续传的次数（默认 4）
	Tick    time.Duration // 监控间隔（默认 250ms）
}

func (p DownloadPolicy) withDefaults() DownloadPolicy {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&p.Warmup, 4*time.Second)
	def(&p.Window, 8*time.Second)
	def(&p.SlowETA, 45*time.Second)
	def(&p.Race, 6*time.Second)
	def(&p.Stall, 20*time.Second)
	def(&p.Total, 15*time.Minute)
	def(&p.Tick, 250*time.Millisecond)
	if p.Window < p.Race+p.Tick {
		p.Window = p.Race + p.Tick // 并行比较要用到比较开始那一刻的样本
	}
	if p.Retries <= 0 {
		p.Retries = 4
	}
	if p.Retries > 32 {
		p.Retries = 32
	}
	return p
}

// raceWin：并行比较时，新路径要比原路径快这么多倍才换过去，免得来回抖。
const raceWin = 1.5

// DownloadMirrors 依次从各镜像下载，大小和 sha256 都对上才改名为最终文件并返回路径和用上的镜像名。
// 每个地址内部：主路径太慢时并行试其他路径、留下快的那条；断开或卡住时从断点续传（服务器不支持 Range 就从头下）。
// 下载完后整份文件重新读一遍算 sha256，不管中途换过几次路径。
// 某个镜像给的文件对不上（被篡改、不完整）就删掉、换下一个；全部失败才报错，校验失败优先报 ErrChecksum。
// 任何失败都不留下可执行文件。
func DownloadMirrors(ctx context.Context, mirrors []Mirror, a Asset, dir string, pol DownloadPolicy, progress func(Progress)) (string, string, []MirrorFailure, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", nil, err
	}
	pol = pol.withDefaults()
	var fails []MirrorFailure
	var checksum error
	for _, m := range mirrors {
		routes := append([]Route{m.Route}, m.Alt...)
		for _, u := range m.URLs {
			path, err := downloadURL(ctx, routes, u, a, dir, pol, progress)
			if err == nil {
				return path, m.Name, fails, nil
			}
			fails = append(fails, MirrorFailure{Mirror: m.Name, Err: err})
			if errors.Is(err, ErrChecksum) && checksum == nil {
				checksum = err
			}
			if ctx.Err() != nil {
				return "", "", fails, ctx.Err()
			}
		}
	}
	if checksum != nil {
		return "", "", fails, checksum
	}
	if len(fails) == 0 {
		return "", "", nil, errors.New("没有下载地址")
	}
	var errs []error
	for _, f := range fails {
		errs = append(errs, fmt.Errorf("%s: %w", f.Mirror, f.Err))
	}
	return "", "", fails, errors.Join(errs...)
}

// Download 是只有一个镜像、一条路径时的 DownloadMirrors（默认策略）。
func Download(ctx context.Context, r Route, urls []string, a Asset, dir string, progress func(Progress)) (string, error) {
	path, _, _, err := DownloadMirrors(ctx, []Mirror{{Name: r.Name, Route: r, URLs: urls}}, a, dir, DownloadPolicy{}, progress)
	return path, err
}

// downloadURL 用一组路径下载一个地址。几条路径拼出来的文件 sha256 对不上时（某条路径给了不同的内容），
// 再逐条路径单独下一遍，这样一条路径被篡改不会连累另一条好的路径。
func downloadURL(ctx context.Context, routes []Route, url string, a Asset, dir string, pol DownloadPolicy, progress func(Progress)) (string, error) {
	path, used, err := fetchAsset(ctx, routes, url, a, dir, pol, progress)
	if err == nil || !errors.Is(err, ErrChecksum) || used < 2 || ctx.Err() != nil {
		return path, err
	}
	slog.Warn("更新包由几条网络路径拼成，校验失败，逐条路径单独重下", "err", err.Error())
	for _, r := range routes {
		p, _, rerr := fetchAsset(ctx, []Route{r}, url, a, dir, pol, progress)
		if rerr == nil {
			return p, nil
		}
		if ctx.Err() != nil {
			return "", rerr
		}
	}
	return "", err
}

type sample struct {
	at  time.Time
	pos int64
}

// leg 是一条路径上的一次请求，从 start 开始往后写。
type leg struct {
	id       int
	route    Route
	start    int64
	launched time.Time
	cancel   context.CancelFunc

	// 以下由 job.mu 保护
	pos      int64
	lastByte time.Time
	samples  []sample
	err      error
}

type job struct {
	f    *os.File
	size int64
	url  string
	name string

	mu      sync.Mutex
	covered int64 // [0, covered) 已连续写好
	events  chan *leg
	wg      sync.WaitGroup // 所有请求的 goroutine；校验前要等它们都退出，免得校验后还有写入
}

// fetchAsset 下载一个地址到临时文件并整份校验。返回实际用过的路径条数。
func fetchAsset(ctx context.Context, routes []Route, url string, a Asset, dir string, pol DownloadPolicy, progress func(Progress)) (string, int, error) {
	if len(routes) == 0 {
		return "", 0, errors.New("没有可用的网络路径")
	}
	ctx, cancelAll := context.WithTimeout(ctx, pol.Total)
	defer cancelAll()
	tmp, err := os.CreateTemp(dir, "download-*.part")
	if err != nil {
		return "", 0, err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	j := &job{f: tmp, size: a.Size, url: url, name: a.Name, events: make(chan *leg, 64)}
	used, err := j.run(ctx, routes, pol, progress)
	cancelAll()
	j.wg.Wait()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded && !errors.Is(err, ErrChecksum) {
			err = fmt.Errorf("下载 %s 超过 %s 仍未完成：%w", a.Name, pol.Total, err)
		}
		return "", used, err
	}
	// 整份重读校验：中途换过路径、续传过也一样逐字节核对。
	if progress != nil {
		progress(Progress{Done: a.Size, Total: a.Size, Verifying: true})
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", used, err
	}
	h := sha256.New()
	n, err := io.Copy(h, tmp)
	if err != nil {
		return "", used, err
	}
	if n != a.Size {
		return "", used, fmt.Errorf("%w：%s 只有 %d 字节，清单写的是 %d", ErrChecksum, a.Name, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return "", used, fmt.Errorf("%w：%s 的 sha256 是 %s，清单写的是 %s", ErrChecksum, a.Name, got, a.SHA256)
	}
	if err := tmp.Sync(); err != nil {
		return "", used, err
	}
	if err := tmp.Close(); err != nil {
		return "", used, err
	}
	final := filepath.Join(dir, "staged-"+a.Name)
	_ = os.Remove(final)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", used, err
	}
	_ = os.Chmod(final, 0o755)
	ok = true
	return final, used, nil
}

// run 是一个地址的下载调度：一条路径在下；它慢了就并行开下一条比一比，留快的；
// 断开或卡住就从已写好的位置在别的（或同一条）路径续传。
func (j *job) run(ctx context.Context, routes []Route, pol DownloadPolicy, progress func(Progress)) (int, error) {
	active := map[int]*leg{}
	usedRoutes := map[string]bool{}
	lost := map[string]bool{}  // 并行比较输掉的路径，不再拿来比
	failed := map[string]int{} // 每条路径断开/卡住的次数
	nextID := 0
	retries := 0
	var raceStart time.Time
	var lastErr error

	launch := func(r Route) {
		nextID++
		lctx, cancel := context.WithCancel(ctx)
		j.mu.Lock()
		from := j.covered
		now := time.Now()
		l := &leg{id: nextID, route: r, start: from, launched: now, cancel: cancel, pos: from, lastByte: now, samples: []sample{{now, from}}}
		j.mu.Unlock()
		active[l.id] = l
		usedRoutes[r.Name] = true
		j.wg.Add(1)
		go j.fetch(lctx, l)
	}
	stop := func(l *leg) {
		l.cancel()
		delete(active, l.id)
	}
	defer func() {
		for _, l := range active {
			l.cancel()
		}
	}()
	// pickRoute 选续传用的路径：断开次数最少的，相同时按原顺序（主路径优先）。
	pickRoute := func(avoid string) Route {
		best := -1
		for i, r := range routes {
			if r.Name == avoid && len(routes) > 1 {
				continue
			}
			if best < 0 || failed[r.Name] < failed[routes[best].Name] {
				best = i
			}
		}
		return routes[best]
	}

	// overall 是 covered 的采样，算整体速度：并行比较、续传时也连续，不随领先路径来回跳。
	began := time.Now()
	overall := []sample{{began, 0}}
	report := func(now time.Time) {
		if progress == nil || len(active) == 0 {
			return
		}
		var primary, probe *leg
		for _, l := range active {
			if primary == nil || l.id < primary.id {
				primary, probe = l, primary
			} else {
				probe = l
			}
		}
		p := Progress{Total: j.size, Route: primary.route.Name}
		if probe != nil {
			p.Probe = probe.route.Name
		}
		j.mu.Lock()
		p.Done = j.covered
		j.mu.Unlock()
		if now.Sub(began) >= pol.Warmup/2 {
			p.Rate = int64(windowRate(overall))
		}
		progress(p)
	}

	launch(routes[0])
	tick := time.NewTicker(pol.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return len(usedRoutes), lastErr
			}
			return len(usedRoutes), ctx.Err()
		case l := <-j.events:
			if _, ok := active[l.id]; !ok {
				continue // 已被取消的路径
			}
			j.mu.Lock()
			err, done := l.err, l.pos >= j.size && l.err == nil
			j.mu.Unlock()
			if done {
				for _, o := range active {
					if o.id != l.id {
						o.cancel()
					}
				}
				active = map[int]*leg{l.id: l}
				report(time.Now()) // 最后一帧：100%
				active = map[int]*leg{}
				if len(usedRoutes) > 1 {
					slog.Info("更新包下载完成", "route", l.route.Name, logx.FileOnly)
				}
				return len(usedRoutes), nil
			}
			delete(active, l.id)
			if errors.Is(err, ErrChecksum) {
				return len(usedRoutes), err
			}
			lastErr = err
			failed[l.route.Name]++
			if len(active) > 0 {
				// 另一条还在下：比较结束，出错的这条算比输了，不再拿来比。
				// 否则一条马上就失败的路径（代理拒绝、连不上）会在每个监控周期被重新拉起，一秒好几次。
				raceStart = time.Time{}
				lost[l.route.Name] = true
				slog.Info("并行试的路径出错，继续用原路径", "route", l.route.Name, "err", err.Error(), logx.FileOnly)
				continue
			}
			if retries >= pol.Retries || ctx.Err() != nil {
				return len(usedRoutes), err
			}
			retries++
			r := pickRoute(l.route.Name)
			j.mu.Lock()
			at := j.covered
			j.mu.Unlock()
			slog.Warn("更新包下载中断，从断点续传", logx.FileOnly, "route", l.route.Name, "next", r.Name, "offset", at, "err", err.Error())
			launch(r)
		case now := <-tick.C:
			j.mu.Lock()
			for _, l := range active {
				l.samples = append(l.samples, sample{now, l.pos})
				trim := 0
				for trim < len(l.samples)-2 && now.Sub(l.samples[trim+1].at) >= pol.Window {
					trim++
				}
				l.samples = l.samples[trim:]
			}
			var stalled []*leg
			for _, l := range active {
				if now.Sub(l.lastByte) >= pol.Stall {
					stalled = append(stalled, l)
				}
			}
			overall = append(overall, sample{now, j.covered})
			trim := 0
			for trim < len(overall)-2 && now.Sub(overall[trim+1].at) >= pol.Window {
				trim++
			}
			overall = overall[trim:]
			j.mu.Unlock()
			report(now)
			for _, l := range stalled {
				// 卡住按断开处理：取消后由 fetch 报 errStall，走上面的续传逻辑。
				j.mu.Lock()
				if l.err == nil {
					l.err = errStall
				}
				j.mu.Unlock()
				l.cancel()
			}
			if len(stalled) > 0 {
				continue
			}
			switch len(active) {
			case 1:
				var l *leg
				for _, x := range active {
					l = x
				}
				if now.Sub(l.launched) < pol.Warmup {
					continue
				}
				alt, ok := nextRoute(routes, l.route.Name, lost)
				if !ok {
					continue
				}
				j.mu.Lock()
				r := windowRate(l.samples)
				remaining := j.size - j.covered
				j.mu.Unlock()
				if r > 0 && time.Duration(float64(remaining)/r*float64(time.Second)) <= pol.SlowETA {
					continue
				}
				slog.Info("更新包下载太慢，并行试另一条网络路径", "route", l.route.Name, "rate", int64(r), "next", alt.Name, logx.FileOnly)
				raceStart = now
				launch(alt)
			case 2:
				if raceStart.IsZero() || now.Sub(raceStart) < pol.Race {
					continue
				}
				var old, nw *leg
				for _, x := range active {
					if old == nil || x.id < old.id {
						old, nw = x, old
					} else {
						nw = x
					}
				}
				j.mu.Lock()
				ro, rn := progressSince(old, raceStart), progressSince(nw, raceStart)
				j.mu.Unlock()
				raceStart = time.Time{}
				if rn > raceWin*ro {
					slog.Info("更新包下载改走更快的路径", "from", old.route.Name, "to", nw.route.Name, "old_bytes", ro, "new_bytes", rn, logx.FileOnly)
					lost[old.route.Name] = true
					stop(old)
				} else {
					slog.Info("另一条路径没有更快，继续用原路径", "route", old.route.Name, "old_bytes", ro, "other", nw.route.Name, "other_bytes", rn, logx.FileOnly)
					lost[nw.route.Name] = true
					stop(nw)
				}
			}
		}
	}
}

// nextRoute 找一条没比输过、不是当前这条的路径。
func nextRoute(routes []Route, cur string, lost map[string]bool) (Route, bool) {
	for _, r := range routes {
		if r.Name != cur && !lost[r.Name] {
			return r, true
		}
	}
	return Route{}, false
}

// windowRate 是样本窗口里的平均速度（字节/秒）。
func windowRate(s []sample) float64 {
	if len(s) < 2 {
		return 0
	}
	dt := s[len(s)-1].at.Sub(s[0].at).Seconds()
	if dt <= 0 {
		return 0
	}
	return float64(s[len(s)-1].pos-s[0].pos) / dt
}

// progressSince 是 l 在 t 之后写了多少字节（l 在 t 之后才开始的，从它的起点算）。
func progressSince(l *leg, t time.Time) float64 {
	base := l.start
	for _, s := range l.samples {
		if !s.at.After(t) {
			base = s.pos
		}
	}
	return float64(l.pos - base)
}

// fetch 在一条路径上从 l.start 起下载，写进文件对应位置。结束（完成、出错、被取消）时发一个事件。
// 事件通道的容量大于一次下载可能开的请求数（续传次数 + 路径数），发送不会阻塞。
func (j *job) fetch(ctx context.Context, l *leg) {
	defer j.wg.Done()
	err := j.fetchLeg(ctx, l)
	j.mu.Lock()
	if l.err == nil { // 卡住时监控已经先写了 errStall
		l.err = err
	}
	j.mu.Unlock()
	select {
	case j.events <- l:
	default:
	}
}

func (j *job) fetchLeg(ctx context.Context, l *leg) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cursor-inner-updater")
	if l.start > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(l.start, 10)+"-")
	}
	resp, err := l.route.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	pos := l.start
	switch {
	case resp.StatusCode == http.StatusPartialContent && l.start > 0:
		var s, e, total int64
		if _, perr := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &s, &e, &total); perr != nil || s != l.start {
			return fmt.Errorf("下载 %s：续传返回的范围不对（%q）", j.name, resp.Header.Get("Content-Range"))
		}
		if total != j.size {
			return fmt.Errorf("%w：%s 服务器说总大小 %d 字节，清单写的是 %d", ErrChecksum, j.name, total, j.size)
		}
	case resp.StatusCode == http.StatusOK:
		pos = 0 // 不支持 Range：从头下，覆盖写同样的内容
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return fmt.Errorf("%w：%s 服务器上的文件比已下载的部分还小", ErrChecksum, j.name)
	default:
		return fmt.Errorf("下载 %s：HTTP %d", j.name, resp.StatusCode)
	}
	j.mu.Lock()
	l.pos, l.start = pos, pos // 回 200 时从头算，比较速度时不吃亏
	j.mu.Unlock()
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if pos+int64(n) > j.size {
				return fmt.Errorf("%w：%s 比清单写的 %d 字节大", ErrChecksum, j.name, j.size)
			}
			if _, err := j.f.WriteAt(buf[:n], pos); err != nil {
				return err
			}
			pos += int64(n)
			j.mu.Lock()
			l.pos, l.lastByte = pos, time.Now()
			if pos > j.covered {
				j.covered = pos
			}
			j.mu.Unlock()
		}
		if rerr == io.EOF {
			if pos != j.size {
				return fmt.Errorf("%w：%s 只有 %d 字节，清单写的是 %d", ErrChecksum, j.name, pos, j.size)
			}
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
