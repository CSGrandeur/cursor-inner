package logx

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugStaysOffTheConsole(t *testing.T) {
	var console, file bytes.Buffer
	h := &split{console: &console, file: &file, warns: map[string]*warnState{}}
	logger := slog.New(h)
	logger.Debug("catalog refresh", "count", 3)
	logger.Info("turn done", "model", "Mine")
	if strings.Contains(console.String(), "catalog") || !strings.Contains(file.String(), "catalog") {
		t.Fatalf("console=%q file=%q", console.String(), file.String())
	}
	if !strings.Contains(console.String(), "turn done") {
		t.Fatal(console.String())
	}
}

func TestWarnCoalescesWithinTenMinutes(t *testing.T) {
	var console bytes.Buffer
	h := &split{console: &console, file: ioDiscard{}, warns: map[string]*warnState{}}
	logger := slog.New(h)
	logger.Warn("leak", "path", "/x")
	logger.Warn("leak", "path", "/x")
	if strings.Count(console.String(), "leak") != 1 {
		t.Fatal(console.String())
	}
	_ = context.Background()
}

func TestConsoleTurnLineReadsAsProse(t *testing.T) {
	var console, file bytes.Buffer
	h := &split{console: &console, file: &file, warns: map[string]*warnState{}}
	logger := slog.New(h)
	logger.Info("▶ Mine", "conversation", "a1b2c3", "turn", 3)
	logger.Info("✓ Mine", "conversation", "a1b2c3", "seconds", 28.1, "tools", 6, "tool_uses", "Read×3 Grep×2 StrReplace×1", "prompt_tokens", 12400, "completion_tokens", 1800, "cache_tokens", 10044)
	logger.Error("✗ Mine", "reason", "429 限流，重试 2 次后失败", "error", "endpoint returned 429")
	logger.Warn("转发给官方的请求带有自定义模型", "path", "/aiserver.v1.AiService/WriteGitCommitMessage", "model", "0123456789abcdef0123456789abcdef", "name", "Mine")
	got := console.String()
	for _, part := range []string{"▶ Mine  对话 a1b2c3  第 3 轮", "✓ Mine  28.1s · 工具 6 次（Read×3 Grep×2 StrReplace×1）· 输入 12.4k / 输出 1.8k · 缓存命中 81%", "✗ Mine  429 限流，重试 2 次后失败", "! 官方接口 /aiserver.v1.AiService/WriteGitCommitMessage 收到自定义模型 Mine"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %s in %q", part, got)
		}
	}
	if strings.Contains(got, "prompt_tokens") || strings.Contains(got, "0123456789abcdef") {
		t.Fatal(got)
	}
	if !strings.Contains(file.String(), "prompt_tokens=12400") || !strings.Contains(file.String(), "model=0123456789abcdef") {
		t.Fatal(file.String())
	}
}

func TestVerboseConsoleMatchesTheFile(t *testing.T) {
	var console, file bytes.Buffer
	h := &split{console: &console, file: &file, verbose: true, warns: map[string]*warnState{}}
	slog.New(h).Info("✓ Mine", "tools", 1)
	if !strings.Contains(console.String(), "tools=1") || !strings.Contains(file.String(), "tools=1") {
		t.Fatalf("console=%q file=%q", console.String(), file.String())
	}
}

func TestRotateMovesALargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inner.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), 5<<20+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Rotate(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal(err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
