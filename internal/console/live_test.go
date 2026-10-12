package console

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// vtConsole 是一个假装支持 VT 的控制台，输出写进临时文件。
func vtConsole(t *testing.T, cols int) (*Console, func() string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	c := &Console{out: out, vt: true, cols: cols, rows: 30}
	return c, func() string {
		raw, _ := os.ReadFile(out.Name())
		return string(raw)
	}
}

// 活动行任何宽度下都不超过 cols-1（折行后 \r 就回不到行首，原地刷新会变成刷屏）。
func TestLiveLineNeverWraps(t *testing.T) {
	info := "6.2 / 14.9 MB · 1.3 MB/s · 剩余约 9 秒 · 代理，并行试默认网络"
	for _, cols := range []int{20, 30, 40, 60, 80, 120, 200} {
		for _, f := range []float64{-1, 0, 0.004, 0.42, 0.999, 1} {
			line := renderLive("下载", f, info, cols)
			if w := width(ansi.ReplaceAllString(line, "")); w > cols-1 {
				t.Fatalf("cols %d frac %v: width %d: %q", cols, f, w, ansi.ReplaceAllString(line, ""))
			}
		}
	}
	wide := ansi.ReplaceAllString(renderLive("下载", 0.42, info, 120), "")
	if !strings.Contains(wide, " 42% ") || !strings.Contains(wide, info) {
		t.Fatalf("%q", wide)
	}
	if narrow := ansi.ReplaceAllString(renderLive("下载", 0.42, info, 30), ""); !strings.Contains(narrow, "42%") {
		t.Fatalf("narrow lost percent: %q", narrow)
	}
}

// 进度条的格数按比例：半格精度，满格时没有轨道色。
func TestLiveBarFill(t *testing.T) {
	line := renderLive("下载", 0.5, "", 80)
	if !strings.Contains(line, barFill+strings.Repeat(" ", barMax/2)+barTrack) {
		t.Fatalf("half bar wrong: %q", line)
	}
	full := renderLive("下载", 1, "", 80)
	if !strings.Contains(full, barFill+strings.Repeat(" ", barMax)+barTrack+reset) || !strings.Contains(full, "100%") {
		t.Fatalf("full bar wrong: %q", full)
	}
}

// 普通日志行和活动行交替：日志写在活动行的位置，活动行重画到下一行；结束时留下总结行。
func TestLiveLineInterleavesWithLogLines(t *testing.T) {
	c, read := vtConsole(t, 100)
	c.Event("before")
	c.Progress("下载", 0.1, "a")
	c.Progress("下载", 0.2, "b")
	c.Event("log-in-between")
	c.Progress("下载", 0.3, "c")
	c.ProgressDone("✓ 下载完成")
	c.Event("after")
	out := read()
	// 第一次出现活动行另起一行；之后原地刷新（\r + 清行），不再换行。
	if !strings.Contains(out, "before"+reset+"\r\n"+signal+" 下载") {
		t.Fatalf("live line not started on a new line: %q", out)
	}
	if strings.Count(out, "\r\n") != 4 { // before、活动行开始、日志后重画活动行、after
		t.Fatalf("unexpected line breaks (%d): %q", strings.Count(out, "\r\n"), out)
	}
	i := strings.Index(out, "log-in-between")
	if i < 0 || !strings.HasSuffix(out[:i], "\r"+clear+cream) {
		t.Fatalf("log line did not replace the live row: %q", out)
	}
	rest := out[i:]
	if !strings.Contains(rest, "\r\n") || !strings.Contains(rest, " b") && !strings.Contains(rest, "b"+reset) {
		t.Fatalf("live line not redrawn after log: %q", rest)
	}
	if !regexp.MustCompile(`\r`+regexp.QuoteMeta(clear+faint)+`[0-9]{2}:[0-9]{2}:[0-9]{2}`+regexp.QuoteMeta(reset+"  "+good+"✓ 下载完成")).MatchString(out) ||
		!strings.HasSuffix(out, "\r\n"+cream+"after"+reset) {
		t.Fatalf("done line or following line wrong: %q", out)
	}
}

// 去掉活动行（没有总结行）后，下一行接着原来的位置写，不留空行。
func TestLiveLineRemovedLeavesNoGap(t *testing.T) {
	c, read := vtConsole(t, 80)
	c.Progress("检查更新", -1, "")
	c.ProgressDone("")
	c.Event("next")
	out := read()
	if !strings.Contains(out, "\r"+clear+"\x1b[1A\r\n"+cream+"next") {
		t.Fatalf("%q", out)
	}
}

// 不是终端：活动行什么都不输出，总结行按普通行打印、没有控制序列。
func TestLiveLinePlainWhenNotTerminal(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	c := New(out, nil)
	if c.Interactive() {
		t.Fatal("temp file reported as interactive")
	}
	c.Progress("下载", 0.5, "x")
	c.ProgressDone("✓ 下载完成")
	raw, _ := os.ReadFile(out.Name())
	if !regexp.MustCompile(`^[0-9]{2}:[0-9]{2}:[0-9]{2}  ✓ 下载完成\n$`).Match(raw) {
		t.Fatalf("%q", raw)
	}
}

// 写日志（Write）时活动行同样被让开，且活动行从不进日志文件。
func TestLiveLineStaysOutOfLogFile(t *testing.T) {
	c, _ := vtConsole(t, 80)
	var file strings.Builder
	c.file = &file
	c.Progress("下载", 0.5, "secret-progress")
	_, _ = c.Write([]byte("日志行\n"))
	if strings.Contains(file.String(), "secret-progress") || !strings.Contains(file.String(), "日志行") {
		t.Fatalf("%q", file.String())
	}
}

// 默认不用活动行：即使是 VT 终端，也要显式打开。
func TestLiveProgressIsOptIn(t *testing.T) {
	c, _ := vtConsole(t, 80)
	if c.Interactive() {
		t.Fatal("live progress must be off by default")
	}
	c.SetLiveProgress(true)
	if !c.Interactive() {
		t.Fatal("SetLiveProgress(true) on a VT console must enable the live line")
	}
}
