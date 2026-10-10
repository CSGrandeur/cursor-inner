// Package logx 把日志分成控制台事件和 inner.log 全文。
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type split struct {
	console io.Writer
	file    io.Writer
	verbose bool
	mu      sync.Mutex
	warns   map[string]*warnState
}

type warnState struct {
	last    time.Time
	extra   int
	flushed bool
}

func Setup(console, file io.Writer, verbose bool) {
	h := &split{console: console, file: file, verbose: verbose, warns: map[string]*warnState{}}
	slog.SetDefault(slog.New(h))
}

func (s *split) Enabled(context.Context, slog.Level) bool { return true }

func (s *split) Handle(_ context.Context, r slog.Record) error {
	line := format(r)
	if s.file != nil {
		fmt.Fprintln(s.file, line)
	}
	emitSinks(r)
	show := r.Level >= slog.LevelInfo || s.verbose
	if r.Level == slog.LevelWarn {
		show = s.admitWarn(r.Message, show)
	}
	if show && s.console != nil {
		text := line
		if !s.verbose {
			text = consoleLine(r)
		}
		fmt.Fprintln(s.console, text)
	}
	return nil
}

func (s *split) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *split) WithGroup(string) slog.Handler      { return s }

func (s *split) admitWarn(key string, show bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.warns[key]
	if st == nil {
		s.warns[key] = &warnState{last: time.Now()}
		return show
	}
	if time.Since(st.last) > 10*time.Minute {
		extra := st.extra
		st.extra = 0
		st.last = time.Now()
		if extra > 0 && s.console != nil {
			fmt.Fprintf(s.console, "%s  ! %s 又出现 %d 次\n", time.Now().Format("15:04:05"), key, extra)
		}
		return show
	}
	st.extra++
	return false
}

func format(r slog.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s", r.Time.Format("2006-01-02 15:04:05"), levelName(r.Level), r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	return b.String()
}

func consoleLine(r slog.Record) string {
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	ts := r.Time.Format("15:04:05")
	msg := r.Message
	switch {
	case strings.HasPrefix(msg, "▶ "):
		return ts + "  " + turnStart(strings.TrimPrefix(msg, "▶ "), attrs)
	case strings.HasPrefix(msg, "✓ "):
		return ts + "  " + turnEnd(strings.TrimPrefix(msg, "✓ "), attrs)
	case strings.HasPrefix(msg, "✗ ") || r.Level >= slog.LevelError:
		name := strings.TrimPrefix(msg, "✗ ")
		reason := attrs["reason"]
		if reason == "" {
			reason = name
		}
		if strings.HasPrefix(msg, "✗ ") && name != "" && reason != name {
			return fmt.Sprintf("%s  ✗ %s  %s", ts, name, reason)
		}
		return fmt.Sprintf("%s  ✗ %s", ts, reason)
	case r.Level == slog.LevelWarn && msg == "转发给官方的请求带有自定义模型":
		name := attrs["name"]
		if name == "" {
			name = "自定义模型"
		}
		return fmt.Sprintf("%s  ! 官方接口 %s 收到自定义模型 %s（10 分钟内不再提示）", ts, attrs["path"], name)
	case r.Level == slog.LevelWarn:
		return fmt.Sprintf("%s  ! %s", ts, msg)
	default:
		return fmt.Sprintf("%s  %s", ts, msg)
	}
}

func turnStart(name string, attrs map[string]string) string {
	if kind := attrs["kind"]; kind != "" {
		return "▶ " + name + "  " + kind
	}
	line := "▶ " + name
	if id := attrs["conversation"]; id != "" {
		line += "  对话 " + id
	}
	if turn := attrs["turn"]; turn != "" && turn != "0" {
		line += "  第 " + turn + " 轮"
	}
	return line
}

func turnEnd(name string, attrs map[string]string) string {
	line := "✓ " + name
	if sec := attrs["seconds"]; sec != "" {
		line += "  " + formatSeconds(sec)
	}
	if attrs["kind"] != "" && attrs["prompt_tokens"] == "" {
		return line
	}
	if tools := attrs["tools"]; tools != "" && tools != "0" {
		line += " · 工具 " + tools + " 次"
		if uses := attrs["tool_uses"]; uses != "" {
			line += "（" + uses + "）"
		}
	}
	sep := " · "
	if strings.HasSuffix(line, "）") {
		sep = "· "
	}
	line += sep + "输入 " + formatTokens(attrs["prompt_tokens"]) + " / 输出 " + formatTokens(attrs["completion_tokens"])
	if n, err := strconv.Atoi(attrs["prompt_tokens"]); err == nil && n > 0 {
		cache, _ := strconv.Atoi(attrs["cache_tokens"])
		line += fmt.Sprintf(" · 缓存命中 %d%%", (cache*100+n/2)/n)
	}
	return line
}

func formatSeconds(raw string) string {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw + "s"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + "s"
}

func formatTokens(raw string) string {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1000 {
		if raw == "" {
			return "0"
		}
		return raw
	}
	return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
}

func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "错误"
	case level >= slog.LevelWarn:
		return "警告"
	case level >= slog.LevelInfo:
		return "信息"
	default:
		return "调试"
	}
}

// Rotate 在文件超过 5MB 时改名为 inner.log.1。
func Rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() < 5<<20 {
		return nil
	}
	_ = os.Remove(path + ".1")
	return os.Rename(path, path+".1")
}

var (
	sinkMu sync.Mutex
	sinks  []func(slog.Record)
)

// AddSink 让额外的记录者也收到每一条日志。只有个人记录版会挂上；发版不调用，保持精简。
func AddSink(fn func(slog.Record)) {
	if fn == nil {
		return
	}
	sinkMu.Lock()
	sinks = append(sinks, fn)
	sinkMu.Unlock()
}

func emitSinks(r slog.Record) {
	sinkMu.Lock()
	fns := make([]func(slog.Record), len(sinks))
	copy(fns, sinks)
	sinkMu.Unlock()
	for _, fn := range fns {
		func() {
			defer func() { _ = recover() }()
			fn(r.Clone())
		}()
	}
}
