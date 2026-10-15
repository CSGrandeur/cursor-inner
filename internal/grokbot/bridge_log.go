package grokbot

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 连接调试日志（个人记录版，或带 --debug / --verbose 启动时打开，见 SetConnDebug）：
//   - 每条桥连接结束时一行：编号、客户端、目标、路由、拨号耗时、时长、两向字节、谁先断、原因。
//   - 每分钟一行汇总：活动连接、等收尾的半关闭连接、本进程 CLOSE_WAIT、这一分钟拨号失败。
// 普通模式下一行都不写。只记主机:端口和错误文本（去掉 URL 里的账号密码），不记请求头、令牌和内容。

var connDebug atomic.Bool

// SetConnDebug 打开或关闭代理桥的连接级调试日志。inner.log 会收下所有级别，
// 所以不能靠 slog 级别区分，普通发版运行时保持关闭。
func SetConnDebug(on bool) { connDebug.Store(on) }

func debugOn() bool { return connDebug.Load() }

// statsEvery 是汇总间隔，测试里改小。
var statsEvery = time.Minute

type bridgeStats struct {
	once     sync.Once
	seq      atomic.Uint64
	active   atomic.Int64
	grace    atomic.Int64 // 一个方向已结束、正在等另一方向（防泄漏的收尾等待）
	opened   atomic.Int64 // 本周期新建
	closed   atomic.Int64 // 本周期结束
	forced   atomic.Int64 // 本周期因收尾超时被强制关闭
	failMu   sync.Mutex
	fails    int
	failHost map[string]int
	failLast string
}

func (s *bridgeStats) dialFailed(target string, err error) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if s.failHost == nil {
		s.failHost = map[string]int{}
	}
	s.fails++
	if len(s.failHost) < 32 {
		s.failHost[target]++
	}
	s.failLast = sanitizeErr(err)
}

// snapshot 取出并清零本周期计数。
func (s *bridgeStats) snapshot() (opened, closed, forced int64, fails int, top, last string) {
	opened, closed, forced = s.opened.Swap(0), s.closed.Swap(0), s.forced.Swap(0)
	s.failMu.Lock()
	defer s.failMu.Unlock()
	fails, last = s.fails, s.failLast
	best := 0
	for h, n := range s.failHost {
		if n > best || (n == best && h < top) {
			top, best = h, n
		}
	}
	if top != "" {
		top = fmt.Sprintf("%s×%d", top, best)
	}
	s.fails, s.failHost, s.failLast = 0, nil, ""
	return
}

// startReporter 每个桥只起一次，进程生命周期内常驻；不在调试级别时每周期只做计数清零。
func (b *Bridge) startReporter() {
	b.stats.once.Do(func() {
		go func() {
			t := time.NewTicker(statsEvery)
			defer t.Stop()
			for range t.C {
				b.reportStats()
			}
		}()
	})
}

func (b *Bridge) reportStats() {
	opened, closed, forced, fails, top, last := b.stats.snapshot()
	if !debugOn() {
		return
	}
	attrs := []any{"bridge", b.name(), "active", b.stats.active.Load(), "grace_wait", b.stats.grace.Load(),
		"opened", opened, "closed", closed, "forced", forced, "close_wait", closeWaitCountOrNA()}
	if fails > 0 {
		attrs = append(attrs, "dial_fail", fails, "dial_fail_top", top, "dial_fail_last", last)
	}
	slog.Debug("bridge stats", attrs...)
}

func (b *Bridge) name() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.HasPrefix(b.key, "codex\n") {
		return "codex"
	}
	return "grok"
}

// proxyKind 只取代理地址的协议，不带主机和认证。
func proxyKind(key string) string {
	if i := strings.LastIndexByte(key, '\n'); i >= 0 {
		key = key[i+1:]
	}
	if i := strings.Index(key, "://"); i > 0 {
		return strings.ToLower(key[:i])
	}
	return "http"
}

var userinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// sanitizeErr 去掉错误文本里 URL 的账号密码。
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil && u.User != nil {
			u.User = nil
			ue = &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
			err = ue
		}
	}
	return userinfoRE.ReplaceAllString(err.Error(), "${1}***@")
}

// pipeResult 是隧道结束时的情况。first 是先结束的一方：client 或 upstream。
type pipeResult struct {
	toUp, toClient int64
	first          string
	forced         bool // 另一方向在收尾时限内没结束，被强制关闭
	err            error
}

func ioErr(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// badRequestKind 把读请求失败归成固定几类，日志里不出现任何来自对端的文本。
func badRequestKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	default:
		return "malformed"
	}
}

func closeWaitCountOrNA() string {
	n := closeWaitCount()
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprint(n)
}

var pid = os.Getpid()
