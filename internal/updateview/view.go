// Package updateview 把自更新的状态快照写到控制台。默认是逐行日志：每个阶段一行，下载进度按百分比步长
// 和时间间隔打印普通行，不依赖原地刷新；控制台打开了活动行（Interactive）时才画一行原地刷新的进度条。
//
// 数据只来自 selfupdate.Updater 的订阅（Watch），和配置页用的是同一份快照，
// 百分比、剩余秒数由 selfupdate 算好，这里和页面只负责排版，所以两边数字一致。
// 这里打印的行只上控制台；inner.log 里只有 selfupdate 自己记的关键事件。
package updateview

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cursor-inner/internal/selfupdate"
)

// Sink 是控制台。*console.Console 实现了它。
type Sink interface {
	Interactive() bool
	Progress(label string, frac float64, info string)
	ProgressDone(line string)
	Line(text string) // 带时间的普通状态行
}

// 非交互输出时的降级节奏：进度每过一个步长打一行，或者距上一行超过间隔也打一行（速度很慢时仍能看到在动）。
const (
	plainStep     = 10 // 百分比步长
	plainInterval = 15 * time.Second
)

// View 根据前后两条快照决定打印什么。不是并发安全的，由 Run 在一个 goroutine 里驱动。
type View struct {
	sink Sink
	now  func() time.Time

	prev       selfupdate.Status
	stageStart time.Time // 当前阶段开始的时间，给没有进度的阶段显示已用时间
	live       bool      // 活动行在用
	plainPct   int       // 非交互模式上一次打印的百分比
	plainAt    time.Time // 非交互模式上一次打印的时间
	plainStage string    // 非交互模式已经打过开始行的阶段
}

func New(sink Sink) *View { return &View{sink: sink, now: time.Now} }

// Run 订阅更新器的状态，直到 ctx 结束。没有进度的阶段（检查、校验、交接）每秒刷新一次已用时间。
func Run(ctx context.Context, u *selfupdate.Updater, sink Sink) {
	w := u.Watch()
	defer w.Close()
	v := New(sink)
	snaps := make(chan selfupdate.Status)
	go func() {
		defer close(snaps)
		for {
			st, err := w.Next(ctx)
			if err != nil {
				return
			}
			select {
			case snaps <- st:
			case <-ctx.Done():
				return
			}
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case st, ok := <-snaps:
			if !ok {
				return
			}
			v.Update(st)
		case <-tick.C:
			v.Tick()
		}
	}
}

func applying(state string) bool {
	switch state {
	case selfupdate.StateDownloading, selfupdate.StateVerifying, selfupdate.StatePreflight, selfupdate.StateWaitingIdle, selfupdate.StateHandover:
		return true
	}
	return false
}

// Update 处理一条新快照。
func (v *View) Update(st selfupdate.Status) {
	prev := v.prev
	v.prev = st
	if st.Seq != 0 && prev.Seq != 0 && st.Seq <= prev.Seq {
		v.prev = prev // 过期快照
		return
	}
	stageChanged := st.State != prev.State
	if stageChanged {
		v.stageStart = v.now()
	}
	switch st.State {
	case selfupdate.StateChecking:
		if stageChanged {
			v.setLive("检查更新", -1, "")
		}
	case selfupdate.StateUpToDate:
		if prev.State == selfupdate.StateChecking {
			v.finish("✓ 已是最新版本 " + st.Current + manifestNote(st))
		}
	case selfupdate.StateAvailable:
		switch {
		case prev.State == selfupdate.StateChecking && !st.CanApply:
			v.finish(fmt.Sprintf("有新版本 %s（当前 %s）%s；此构建没有内置更新公钥，请到 Releases 页面手动下载", st.Latest, st.Current, manifestNote(st)))
		case prev.State == selfupdate.StateChecking:
			v.finish(fmt.Sprintf("有新版本 %s（当前 %s）%s；在配置页点“更新”安装", st.Latest, st.Current, manifestNote(st)))
		case applying(prev.State):
			// 失败后又回到可更新：失败那一行已经打过（state=failed）。这里只收尾活动行。
			v.finish("")
		}
	case selfupdate.StateFailed:
		reason := selfupdate.UserMessage(st.ErrorCode, true)
		switch {
		case applying(prev.State):
			v.finish("✗ 更新失败：" + reason + "；当前版本继续运行")
		case prev.State == selfupdate.StateChecking:
			v.finish("✗ 检查更新失败：" + reason)
		}
	case selfupdate.StateDownloading:
		v.downloading(prev, st)
	case selfupdate.StateVerifying:
		if stageChanged {
			v.finish(fmt.Sprintf("✓ 下载完成 %s · 用时 %s%s · %s", Size(st.Total), Duration(time.Duration(st.DLMillis)*time.Millisecond), avgNote(st), RouteName(st.DLRoute)))
			v.setLive("校验", -1, "核对大小和 sha256")
		}
	case selfupdate.StatePreflight:
		if stageChanged {
			if prev.State == selfupdate.StateVerifying {
				v.finish("✓ 大小和 sha256 与签名清单一致")
			}
			v.setLive("自检", -1, "试运行新版本 "+st.Latest+" --version")
		}
	case selfupdate.StateWaitingIdle:
		if stageChanged {
			v.finishStage(prev)
			v.setLive("等待", -1, "自定义模型回合进行中，结束后再交接")
		}
	case selfupdate.StateHandover:
		if stageChanged {
			v.finishStage(prev)
			v.setLive("热交接", -1, "新版本 "+st.Latest+" 接过本机端口，Grok 和 Cursor 不需要重启")
		}
	case selfupdate.StateDone:
		if stageChanged {
			v.finish("✓ 已交接给 " + st.Latest + "，本进程处理完进行中的连接后退出")
		}
	}
}

// finishStage 给没有进度的阶段收尾。
func (v *View) finishStage(prev selfupdate.Status) {
	switch prev.State {
	case selfupdate.StatePreflight:
		v.finish("✓ 新版本自检通过")
	case selfupdate.StateWaitingIdle:
		v.finish("✓ 回合已结束")
	default:
		v.finish("")
	}
}

func (v *View) downloading(prev, st selfupdate.Status) {
	if prev.State != selfupdate.StateDownloading {
		if prev.State == selfupdate.StateVerifying {
			v.finish("✗ 下载的文件和签名清单对不上，已删除，换下一个源重新下载")
		}
		v.event(fmt.Sprintf("▶ 更新到 %s：下载 %s（%s）", st.Latest, st.Asset, Size(st.Total)))
		v.plainPct, v.plainAt = 0, v.now()
	} else {
		// 路径变化（并行比较、换路径、续传换路径）各打一行，进度条只显示当前状态。
		switch {
		case st.DLProbe != "" && prev.DLProbe == "":
			v.event(fmt.Sprintf("… %s太慢%s，并行试%s", RouteName(st.DLRoute), rateNote(prev.Speed), RouteName(st.DLProbe)))
		case st.DLProbe == "" && prev.DLProbe != "" && st.DLRoute != prev.DLRoute:
			v.event("↻ 改走" + RouteName(st.DLRoute) + "，从已下载的位置接着下")
		case st.DLProbe == "" && prev.DLProbe != "":
			v.event(fmt.Sprintf("… %s没有更快，继续走%s", RouteName(prev.DLProbe), RouteName(st.DLRoute)))
		case st.DLRoute != prev.DLRoute && prev.DLRoute != "":
			v.event("↻ 改走" + RouteName(st.DLRoute) + "，从断点续传")
		}
	}
	info := Info(st)
	if v.sink.Interactive() {
		v.setLive("下载", frac(st), info)
		return
	}
	// 降级：跨过一个步长或隔了足够久才打一行；100% 由“下载完成”那一行代替。
	now := v.now()
	step := st.Percent / plainStep * plainStep
	if st.Percent < 100 && (step > v.plainPct || now.Sub(v.plainAt) >= plainInterval) && st.Done > 0 {
		v.plainPct, v.plainAt = max(step, v.plainPct), now
		v.sink.Line(fmt.Sprintf("  下载 %d%% · %s", st.Percent, info))
	}
}

// Tick 每秒调用一次：没有进度的阶段刷新已用时间。
func (v *View) Tick() {
	if !v.live || !v.sink.Interactive() {
		return
	}
	switch v.prev.State {
	case selfupdate.StateChecking, selfupdate.StateVerifying, selfupdate.StatePreflight, selfupdate.StateWaitingIdle, selfupdate.StateHandover:
		label, info := liveFor(v.prev)
		if s := v.now().Sub(v.stageStart); s >= 2*time.Second {
			info += fmt.Sprintf(" · %d 秒", int(s.Seconds()))
		}
		v.sink.Progress(label, -1, info)
	}
}

func liveFor(st selfupdate.Status) (string, string) {
	switch st.State {
	case selfupdate.StateChecking:
		return "检查更新", ""
	case selfupdate.StateVerifying:
		return "校验", "核对大小和 sha256"
	case selfupdate.StatePreflight:
		return "自检", "试运行新版本 " + st.Latest + " --version"
	case selfupdate.StateWaitingIdle:
		return "等待", "自定义模型回合进行中，结束后再交接"
	default:
		return "热交接", "新版本 " + st.Latest + " 接过本机端口，Grok 和 Cursor 不需要重启"
	}
}

func (v *View) setLive(label string, f float64, info string) {
	if !v.sink.Interactive() {
		// 逐行模式：没有进度可言的阶段（校验、自检、等待、交接）开始时打一行，检查更新只打结果。
		if f < 0 && label != "检查更新" && label != v.plainStage {
			v.plainStage = label
			line := "… " + label
			if info != "" {
				line += "：" + info
			}
			v.sink.Line(line)
		}
		return
	}
	v.live = true
	v.sink.Progress(label, f, info)
}

// finish 收起活动行；line 不为空时留下这一行。
func (v *View) finish(line string) {
	v.plainStage = ""
	if v.live {
		v.live = false
		v.sink.ProgressDone(line)
		return
	}
	v.event(line)
}

func (v *View) event(line string) {
	if line != "" {
		v.sink.Line(line)
	}
}

func frac(st selfupdate.Status) float64 {
	if st.Total <= 0 {
		return 0
	}
	return float64(st.Percent) / 100
}

// Info 是进度条右边的文字：“6.2 / 14.9 MB · 1.3 MB/s · 剩余约 9 秒 · 代理”。
// 配置页 page.html 里的 upInfo 按同样的规则排版。
func Info(st selfupdate.Status) string {
	parts := []string{SizePair(st.Done, st.Total)}
	if st.Speed > 0 {
		parts = append(parts, Speed(st.Speed))
		if st.ETA > 0 {
			parts = append(parts, "剩余约 "+ETA(st.ETA))
		}
	} else {
		parts = append(parts, "测速中")
	}
	route := RouteName(st.DLRoute)
	if st.DLProbe != "" {
		route += "，并行试" + RouteName(st.DLProbe)
	}
	if route != "" {
		parts = append(parts, route)
	}
	return strings.Join(parts, " · ")
}

func manifestNote(st selfupdate.Status) string {
	var parts []string
	if st.Source != "" {
		parts = append(parts, "清单 "+SourceName(st.Source)+" · "+RouteName(st.Route))
	}
	if st.Signed {
		sig := "签名已验证"
		if len(st.KeyID) >= 8 {
			sig += "（密钥 " + st.KeyID[:8] + "）"
		}
		parts = append(parts, sig)
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

func avgNote(st selfupdate.Status) string {
	if st.DLMillis <= 0 || st.Total <= 0 {
		return ""
	}
	return " · 平均 " + Speed(st.Total*1000/st.DLMillis)
}

func rateNote(bps int64) string {
	if bps <= 0 {
		return ""
	}
	return "（" + Speed(bps) + "）"
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "未知错误"
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", "；")), " ")
}
