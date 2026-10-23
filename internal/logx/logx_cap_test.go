package logx

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileCapRotatesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inner.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oldChecks := capChecks
	capChecks = 1
	SetFileCap(1000)
	t.Cleanup(func() { SetFileCap(0); capChecks = oldChecks })
	h := &split{file: f, warns: map[string]*warnState{}}
	line := strings.Repeat("x", 100)
	for i := 0; i < 25; i++ {
		_ = h.Handle(nil, recordOf(line))
	}
	st, _ := os.Stat(path)
	if st.Size() >= 1200 {
		t.Fatalf("inner.log 应被清空续写，当前 %d", st.Size())
	}
	if st1, err := os.Stat(path + ".1"); err != nil || st1.Size() < 1000 {
		t.Fatalf(".1 应保留旧内容：%v", err)
	}
}

func TestFileCapOffByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inner.log")
	f, _ := os.Create(path)
	defer f.Close()
	h := &split{file: f, warns: map[string]*warnState{}}
	for i := 0; i < 50; i++ {
		_ = h.Handle(nil, recordOf(strings.Repeat("y", 100)))
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("发版默认不应运行中轮转")
	}
}

func recordOf(msg string) slog.Record {
	return slog.NewRecord(time.Now(), slog.LevelDebug, msg, 0)
}
