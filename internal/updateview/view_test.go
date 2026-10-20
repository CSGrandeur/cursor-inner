package updateview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/selfupdate"
)

type fakeSink struct {
	interactive bool
	lines       []string // 留在屏幕上的行（Event 与 ProgressDone 的总结行）
	live        []string // 每次刷新活动行的内容
	liveOn      bool
}

func (f *fakeSink) Interactive() bool { return f.interactive }
func (f *fakeSink) Progress(label string, frac float64, info string) {
	f.liveOn = true
	f.live = append(f.live, fmt.Sprintf("%s|%.2f|%s", label, frac, info))
}
func (f *fakeSink) ProgressDone(line string) {
	f.liveOn = false
	if line != "" {
		f.lines = append(f.lines, line)
	}
}
func (f *fakeSink) Line(text string) { f.lines = append(f.lines, text) }

// 一次完整更新的快照序列：检查 → 下载（默认网络慢，并行试代理，换到代理）→ 校验 → 自检 → 交接 → 完成。
func upgradeSnapshots() []selfupdate.Status {
	base := selfupdate.Status{Current: "v0.5.3", Latest: "v0.5.4", Source: "github", Route: "proxy", Signed: true, KeyID: "eb68418a53240b13", CanApply: true}
	var out []selfupdate.Status
	seq := uint64(0)
	add := func(fn func(*selfupdate.Status)) {
		s := base
		fn(&s)
		seq++
		s.Seq = seq
		if s.Total > 0 {
			s.Percent = int(s.Done * 100 / s.Total)
			if s.Speed > 0 && s.Total > s.Done {
				s.ETA = (s.Total - s.Done + s.Speed - 1) / s.Speed
			}
		}
		out = append(out, s)
		base = s
	}
	add(func(s *selfupdate.Status) { s.State = selfupdate.StateChecking })
	add(func(s *selfupdate.Status) { s.State = selfupdate.StateAvailable })
	total := int64(15 << 20)
	add(func(s *selfupdate.Status) {
		s.State, s.Asset, s.Total = selfupdate.StateDownloading, "cursor-inner-v0.5.4-windows-amd64.exe", total
	})
	for i := int64(1); i <= 4; i++ {
		add(func(s *selfupdate.Status) { s.Done, s.Speed, s.DLRoute = i*64<<10, 64<<10, "default" })
	}
	add(func(s *selfupdate.Status) { s.DLProbe = "proxy" })
	add(func(s *selfupdate.Status) { s.DLProbe, s.DLRoute = "", "proxy" })
	for p := int64(5); p <= 100; p += 5 {
		add(func(s *selfupdate.Status) { s.Done, s.Speed = total*p/100, 3<<20 })
	}
	add(func(s *selfupdate.Status) {
		s.State, s.Speed, s.DLMillis = selfupdate.StateVerifying, 0, 7300
	})
	add(func(s *selfupdate.Status) { s.State, s.Mirror = selfupdate.StatePreflight, "github" })
	add(func(s *selfupdate.Status) { s.State = selfupdate.StateHandover })
	add(func(s *selfupdate.Status) { s.State = selfupdate.StateDone })
	return out
}

func TestInteractiveUpgradeStages(t *testing.T) {
	sink := &fakeSink{interactive: true}
	v := New(sink)
	for _, st := range upgradeSnapshots() {
		v.Update(st)
	}
	got := strings.Join(sink.lines, "\n")
	want := []string{
		"有新版本 v0.5.4（当前 v0.5.3） · 清单 GitHub · 代理 · 签名已验证（密钥 eb68418a）；在配置页点“更新”安装",
		"▶ 更新到 v0.5.4：下载 cursor-inner-v0.5.4-windows-amd64.exe（15.0 MB）",
		"… 默认网络太慢（64 KB/s），并行试代理",
		"↻ 改走代理，从已下载的位置接着下",
		"✓ 下载完成 15.0 MB · 用时 7.3 秒 · 平均 2.1 MB/s · 代理",
		"✓ 大小和 sha256 与签名清单一致",
		"✓ 新版本自检通过",
		"✓ 已交接给 v0.5.4，本进程处理完进行中的连接后退出",
	}
	if strings.Join(want, "\n") != got {
		t.Fatalf("lines:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if sink.liveOn {
		t.Fatal("live line left open after done")
	}
	// 进度条帧：标签、比例与快照里的百分比一致，文字与 Info 相同。
	var sawProbe, sawFull bool
	for _, l := range sink.live {
		sawProbe = sawProbe || strings.Contains(l, "默认网络，并行试代理")
		sawFull = sawFull || strings.HasPrefix(l, "下载|1.00|15.0 / 15.0 MB")
	}
	if !sawProbe || !sawFull {
		t.Fatalf("live frames: %v", sink.live)
	}
}

// 非交互（重定向）：不画活动行；进度按 10% 步长最多打 9 行，阶段行和交互模式相同。
func TestPlainOutputStepsAndNoLiveLine(t *testing.T) {
	sink := &fakeSink{interactive: false}
	v := New(sink)
	clock := time.Unix(0, 0)
	v.now = func() time.Time { return clock }
	for _, st := range upgradeSnapshots() {
		clock = clock.Add(100 * time.Millisecond)
		v.Update(st)
	}
	if len(sink.live) != 0 {
		t.Fatalf("live frames in plain mode: %v", sink.live)
	}
	var progress []string
	for _, l := range sink.lines {
		if strings.HasPrefix(l, "  下载 ") {
			progress = append(progress, l)
		}
	}
	if len(progress) == 0 || len(progress) > 9 {
		t.Fatalf("progress lines: %v", progress)
	}
	for i, l := range progress {
		if i > 0 && l == progress[i-1] {
			t.Fatalf("duplicate %q", l)
		}
		if strings.Contains(l, "100%") {
			t.Fatalf("100%% printed instead of the done line: %q", l)
		}
	}
	all := strings.Join(sink.lines, "\n")
	for _, part := range []string{"▶ 更新到 v0.5.4", "↻ 改走代理", "✓ 下载完成", "… 校验：核对大小和 sha256", "… 热交接", "✓ 已交接给 v0.5.4"} {
		if !strings.Contains(all, part) {
			t.Fatalf("missing %q in\n%s", part, all)
		}
	}
}

// 慢到 10% 都要很久时，按时间间隔也打一行，让人知道没卡住。
func TestPlainOutputIntervalWhenSlow(t *testing.T) {
	sink := &fakeSink{}
	v := New(sink)
	clock := time.Unix(0, 0)
	v.now = func() time.Time { return clock }
	st := selfupdate.Status{State: selfupdate.StateAvailable, Latest: "v2", Seq: 1}
	v.Update(st)
	st.State, st.Total, st.Asset = selfupdate.StateDownloading, 100<<20, "a.exe"
	for i := int64(1); i <= 60; i++ {
		clock = clock.Add(time.Second)
		st.Seq++
		st.Done, st.Speed = i*(64<<10), 64<<10
		st.Percent = int(st.Done * 100 / st.Total)
		v.Update(st)
	}
	n := 0
	for _, l := range sink.lines {
		if strings.HasPrefix(l, "  下载 ") {
			n++
		}
	}
	if n < 3 || n > 5 { // 60 秒里每 15 秒一行
		t.Fatalf("%d interval lines: %v", n, sink.lines)
	}
}

func TestFailureLines(t *testing.T) {
	sink := &fakeSink{interactive: true}
	v := New(sink)
	v.Update(selfupdate.Status{State: selfupdate.StateAvailable, Latest: "v2", Seq: 1})
	v.Update(selfupdate.Status{State: selfupdate.StateDownloading, Latest: "v2", Total: 100, Seq: 2})
	v.Update(selfupdate.Status{State: selfupdate.StateFailed, Latest: "v2", Error: "github：断了\ngitee：404", Seq: 3})
	v.Update(selfupdate.Status{State: selfupdate.StateAvailable, Latest: "v2", Error: "github：断了\ngitee：404", Seq: 4})
	v.Update(selfupdate.Status{State: selfupdate.StateChecking, Seq: 5})
	v.Update(selfupdate.Status{State: selfupdate.StateFailed, Error: "连不上", Seq: 6})
	got := strings.Join(sink.lines, "\n")
	if !strings.Contains(got, "✗ 更新失败：github：断了；gitee：404；当前版本继续运行") || !strings.Contains(got, "✗ 检查更新失败：连不上") || sink.liveOn {
		t.Fatalf("%s (live %v)", got, sink.liveOn)
	}
	// 过期快照被忽略。
	n := len(sink.lines)
	v.Update(selfupdate.Status{State: selfupdate.StateDone, Latest: "v2", Seq: 3})
	if len(sink.lines) != n {
		t.Fatal("stale snapshot printed")
	}
}

// 排版规则：和 page.html 的 upSize / upSizePair / upSpeed / upEta 是同一张表（web 包的测试用 node 核对页面那一边）。
var formatTable = []struct {
	Done, Total, Speed, ETA  int64
	Pair, SpeedText, ETAText string
}{
	{0, 0, 0, 0, "0 KB", "0 KB/s", "0 秒"},
	{300, 900, 1, 1, "0 / 1 KB", "1 KB/s", "1 秒"},
	{6501171, 15623782, 1363149, 7, "6.2 / 14.9 MB", "1.3 MB/s", "7 秒"},
	{524288, 1048576, 65536, 89, "0.5 / 1.0 MB", "64 KB/s", "89 秒"},
	{1048575, 1048575, 1048575, 90, "1024 / 1024 KB", "1024 KB/s", "2 分钟"},
	{0, 52428800, 1572864, 3600, "0.0 / 50.0 MB", "1.5 MB/s", "60 分钟"},
}

func TestFormatTable(t *testing.T) {
	for _, c := range formatTable {
		if got := SizePair(c.Done, c.Total); got != c.Pair {
			t.Fatalf("SizePair(%d,%d)=%q want %q", c.Done, c.Total, got, c.Pair)
		}
		if got := Speed(c.Speed); got != c.SpeedText {
			t.Fatalf("Speed(%d)=%q want %q", c.Speed, got, c.SpeedText)
		}
		if got := ETA(c.ETA); got != c.ETAText {
			t.Fatalf("ETA(%d)=%q want %q", c.ETA, got, c.ETAText)
		}
	}
	if Duration(7300*time.Millisecond) != "7.3 秒" || Duration(125*time.Second) != "2 分 05 秒" {
		t.Fatal(Duration(7300*time.Millisecond), Duration(125*time.Second))
	}
}

func TestInfoShowsMeasuringBeforeSpeed(t *testing.T) {
	got := Info(selfupdate.Status{State: selfupdate.StateDownloading, Done: 0, Total: 15 << 20, DLRoute: "default"})
	if got != "0.0 / 15.0 MB · 测速中 · 默认网络" {
		t.Fatal(got)
	}
}

// 序号不大于上一条的快照是过期的（例如重连后先收到的旧快照），丢弃，不回退显示。
func TestStaleSnapshotIgnored(t *testing.T) {
	f := &fakeSink{interactive: true}
	v := New(f)
	snaps := upgradeSnapshots()
	var at int
	for i, s := range snaps {
		v.Update(s)
		if s.State == selfupdate.StateDownloading && s.Percent >= 50 {
			at = i
			break
		}
	}
	lines, live := len(f.lines), len(f.live)
	v.Update(snaps[1]) // 旧的“有新版本”
	v.Update(snaps[at])
	if len(f.lines) != lines || len(f.live) != live {
		t.Fatalf("stale snapshot changed output: %q %q", f.lines[lines:], f.live[live:])
	}
	v.Update(snaps[at+1])
	if len(f.live) != live+1 {
		t.Fatalf("fresh snapshot not rendered")
	}
}
