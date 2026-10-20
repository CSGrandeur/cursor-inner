package program

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"cursor-inner/internal/selfupdate"
	"cursor-inner/internal/updateview"
)

// 通知区域图标的纯逻辑：状态怎么映射成图标和提示文字、菜单有哪些项、什么时候弹通知。
// 不碰 Win32，在所有平台上都能单测；tray_windows.go 只负责把结果交给 Shell_NotifyIcon。

type trayLevel int

const (
	trayNormal   trayLevel = iota
	trayWarn               // 有需要注意的问题：接管或代理出错、TUN 回退、自定义模型被带给官方
	trayUpdating           // 正在下载、校验、自检或交接新版本
)

// trayTipMax 是 NOTIFYICONDATA.szTip 能放下的 UTF-16 单元数（128，含结尾的 0）。
const trayTipMax = 127

// trayInput 是一次刷新要看的全部状态。
type trayInput struct {
	Version string
	Debug   bool
	Zh      bool
	Update  selfupdate.Status
	Alerts  []string // 已按界面语言选好的文字
}

// trayLook 决定图标状态和提示文字。更新进行中优先于警告：更新只持续几十秒，警告在更新后仍会显示。
func trayLook(in trayInput) (trayLevel, string) {
	level := trayNormal
	if len(in.Alerts) > 0 {
		level = trayWarn
	}
	if updateBusy(in.Update.State) {
		level = trayUpdating
	}
	head := "cursor-inner " + in.Version
	if in.Debug {
		head += pick(in.Zh, " · 调试实例", " · debug")
	}
	lines := []string{head}
	if line := updateLine(in.Update, in.Zh); line != "" {
		lines = append(lines, line)
	}
	if n := len(in.Alerts); n > 0 {
		line := "⚠ " + oneLineText(in.Alerts[0])
		if n > 1 {
			line += pick(in.Zh, fmt.Sprintf("（另有 %d 项）", n-1), fmt.Sprintf(" (+%d more)", n-1))
		}
		lines = append(lines, line)
	}
	return level, fitTip(lines, trayTipMax)
}

func updateBusy(state string) bool {
	switch state {
	case selfupdate.StateDownloading, selfupdate.StateVerifying, selfupdate.StatePreflight,
		selfupdate.StateWaitingIdle, selfupdate.StateHandover, selfupdate.StateDone:
		return true
	}
	return false
}

// updateLine 是提示文字里的更新一行，数字和配置页、控制台用同一份快照（percent、eta 由 selfupdate 算好）。
func updateLine(st selfupdate.Status, zh bool) string {
	to := st.Latest
	switch st.State {
	case selfupdate.StateChecking:
		return pick(zh, "正在检查更新", "Checking for updates")
	case selfupdate.StateAvailable:
		return pick(zh, "有新版本 "+to, to+" is available")
	case selfupdate.StateDownloading:
		parts := []string{strconv.Itoa(st.Percent) + "%"}
		if st.Speed > 0 {
			parts = append(parts, updateview.Speed(st.Speed))
			if st.ETA > 0 {
				parts = append(parts, pick(zh, "剩余约 ", "")+etaText(st.ETA, zh)+pick(zh, "", " left"))
			}
		}
		return pick(zh, "更新到 "+to+"：下载 ", "Updating to "+to+": downloading ") + strings.Join(parts, " · ")
	case selfupdate.StateVerifying:
		return pick(zh, "更新到 "+to+"：校验", "Updating to "+to+": verifying")
	case selfupdate.StatePreflight:
		return pick(zh, "更新到 "+to+"：自检", "Updating to "+to+": self-test")
	case selfupdate.StateWaitingIdle:
		return pick(zh, "更新到 "+to+"：等模型回合结束", "Updating to "+to+": waiting for the model turn")
	case selfupdate.StateHandover, selfupdate.StateDone:
		return pick(zh, "更新到 "+to+"：热交接", "Updating to "+to+": handing over")
	case selfupdate.StateFailed:
		if to == "" || to == st.Current {
			return pick(zh, "检查更新失败", "Update check failed")
		}
		return pick(zh, "更新到 "+to+" 失败，当前版本继续运行", "Update to "+to+" failed; still running this version")
	}
	return ""
}

func etaText(sec int64, zh bool) string {
	if sec < 90 {
		return strconv.FormatInt(sec, 10) + pick(zh, " 秒", " s")
	}
	return strconv.FormatInt((sec+59)/60, 10) + pick(zh, " 分钟", " min")
}

// fitTip 把几行拼成提示文字，超长时从最后一行开始截断（行尾加省略号），首行版本号总是留着。
func fitTip(lines []string, limit int) string {
	for {
		tip := strings.Join(lines, "\n")
		if utf16Len(tip) <= limit {
			return tip
		}
		last := len(lines) - 1
		room := limit - utf16Len(strings.Join(lines[:last], "\n"))
		if last > 0 {
			room-- // 换行
		}
		if room < 4 && last > 0 {
			lines = lines[:last]
			continue
		}
		lines[last] = truncUTF16(lines[last], room)
	}
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// truncUTF16 截到 limit 个 UTF-16 单元以内，结尾放一个省略号；不会把代理对切成两半。
func truncUTF16(s string, limit int) string {
	if utf16Len(s) <= limit {
		return s
	}
	if limit <= 1 {
		return ""
	}
	n := 0
	var b strings.Builder
	for _, r := range s {
		w := len(utf16.Encode([]rune{r}))
		if n+w > limit-1 {
			break
		}
		b.WriteRune(r)
		n += w
	}
	return b.String() + "…"
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func pick(zh bool, cn, en string) string {
	if zh {
		return cn
	}
	return en
}

// 菜单项编号。0 留给“没选”（TrackPopupMenu 返回 0）。
const (
	menuOpen    = 1
	menuConsole = 2
	menuUpdate  = 3
	menuQuit    = 4
)

type trayItem struct {
	ID       uintptr
	Text     string
	Sep      bool
	Default  bool // 加粗，表示左键单击做的就是这一项
	Disabled bool
}

// trayMenu 是右键菜单。控制台显示时第二项是“隐藏”，隐藏时是“显示”；更新进行中“检查更新”变灰。
func trayMenu(zh, consoleShown, canCheck, updating bool) []trayItem {
	console := pick(zh, "显示控制台窗口", "Show console window")
	if consoleShown {
		console = pick(zh, "隐藏控制台窗口", "Hide console window")
	}
	items := []trayItem{
		{ID: menuOpen, Text: pick(zh, "打开配置页", "Open settings"), Default: true},
		{ID: menuConsole, Text: console},
	}
	if canCheck {
		check := trayItem{ID: menuUpdate, Text: pick(zh, "检查更新", "Check for updates")}
		if updating {
			check.Text, check.Disabled = pick(zh, "正在更新…", "Updating…"), true
		}
		items = append(items, check)
	}
	return append(items, trayItem{Sep: true}, trayItem{ID: menuQuit, Text: pick(zh, "退出", "Quit")})
}

// trayActions 是菜单项和左键要做的事，由 program 注入。
type trayActions struct {
	Open        func(page string)
	ShowConsole func()
	HideConsole func()
	Check       func()
	Quit        func()
}

// trayDispatch 执行一个菜单项。consoleShown 是弹出菜单时控制台的状态（菜单上显示的那一项）。
func trayDispatch(id uintptr, consoleShown bool, a trayActions) {
	call := func(fn func()) {
		if fn != nil {
			fn()
		}
	}
	switch id {
	case menuOpen:
		if a.Open != nil {
			a.Open("")
		}
	case menuConsole:
		if consoleShown {
			call(a.HideConsole)
		} else {
			call(a.ShowConsole)
		}
	case menuUpdate:
		if a.Check != nil {
			go a.Check() // 检查和确认框不能卡住托盘的消息循环
		}
	case menuQuit:
		call(a.Quit)
	}
}

// balloon 是一条气泡通知。
type balloon struct {
	Title string
	Text  string
	Warn  bool
	Page  string // 点气泡打开配置页的哪个标签（URL hash），空则默认标签
}

// 配置页标签，对应 page.html 的 URL hash。
const (
	pageModels  = "models"
	pageNetwork = "network"
	pageUpdate  = "update"
	pageGeneral = "general"
)

// leakRepeat 是同一类泄漏告警再次弹通知的最短间隔；图标和提示文字不受它限制。
const leakRepeat = 30 * time.Minute

// trayNotifier 决定什么时候弹通知：更新失败、新的泄漏告警。更新完成由交接确认后的新进程直接弹。
// 不是并发安全的，由托盘的刷新循环驱动。
type trayNotifier struct {
	zh, debug bool

	prev     string               // 上一条更新快照的状态
	leaks    int                  // 已经见过的泄漏计数
	leakSeen bool                 // 已经拿到过第一次计数（启动前就有的不弹）
	shown    map[string]time.Time // 告警文字 → 上次弹出的时间
}

func newTrayNotifier(zh, debug bool) *trayNotifier {
	return &trayNotifier{zh: zh, debug: debug, shown: map[string]time.Time{}}
}

func (n *trayNotifier) title() string {
	if n.debug {
		return pick(n.zh, "cursor-inner（调试实例）", "cursor-inner (debug)")
	}
	return "cursor-inner"
}

// update 看一条新的更新快照。只有从“正在更新”掉到失败才弹；检查失败不弹（不打扰）。
func (n *trayNotifier) update(st selfupdate.Status) *balloon {
	prev := n.prev
	n.prev = st.State
	if st.State != selfupdate.StateFailed || !updateBusy(prev) {
		return nil
	}
	reason := oneLineText(st.Error)
	if reason == "" {
		reason = pick(n.zh, "未知错误", "unknown error")
	}
	text := pick(n.zh, "没能更新到 "+st.Latest+"："+reason+"。当前版本继续运行。",
		"Could not update to "+st.Latest+": "+reason+". Still running the current version.")
	return &balloon{Title: n.title(), Text: text, Warn: true, Page: pageUpdate}
}

// leak 看自定义模型被带给官方的累计次数。计数增加并且距上次弹出超过 leakRepeat 才弹。
func (n *trayNotifier) leak(count int, name string, now time.Time) *balloon {
	first := !n.leakSeen
	n.leakSeen = true
	grew := count > n.leaks
	n.leaks = count
	if first || !grew {
		return nil
	}
	return n.once("leak", pageModels, now, pick(n.zh, "有请求带着自定义模型“"+name+"”转给了官方。打开配置页查看。",
		"A request carrying the custom model \""+name+"\" was forwarded to the official service. Open settings for details."))
}

// alert 看一条需要弹通知的告警（例如 TUN 回退、官方端点可能泄漏）。同一文字 leakRepeat 内只弹一次。
func (n *trayNotifier) alert(text string, now time.Time) *balloon {
	return n.once("alert:"+text, pageNetwork, now, text)
}

func (n *trayNotifier) once(key, page string, now time.Time, text string) *balloon {
	if at, ok := n.shown[key]; ok && now.Sub(at) < leakRepeat {
		return nil
	}
	n.shown[key] = now
	return &balloon{Title: n.title(), Text: text, Warn: true, Page: page}
}

// updatedBalloon 是交接确认后新进程弹的“已更新”。
func updatedBalloon(zh, debug bool, from, to string) balloon {
	n := trayNotifier{zh: zh, debug: debug}
	return balloon{Title: n.title(), Text: pick(zh, "已从 "+from+" 更新到 "+to+"，Grok 和 Cursor 无需重启。",
		"Updated from "+from+" to "+to+". Grok and Cursor keep running."), Page: pageUpdate}
}
