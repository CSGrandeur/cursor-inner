package console

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// 底部活动行：控制台最后一行留给一条原地刷新的状态（自更新的进度条）。
// 普通日志行照常往上滚：写日志时先擦掉活动行，把日志写在它的位置，再在下一行重画活动行，
// 所以两者不会互相打碎。活动行只上屏，不写 inner.log。

const (
	barFill  = "\x1b[48;2;255;90;43m" // 已完成部分：背景色块（空格），不依赖字体里方块字符的宽度
	barTrack = "\x1b[48;2;54;52;48m"  // 未完成部分
	barHalf  = "\x1b[38;2;255;90;43m" // 半格：前景色的 ▌ 叠在轨道色上
	barMin   = 10                     // 进度条最少格数，再窄就只显示文字
	barMax   = 28
)

// SetLiveProgress 打开底部活动行。默认关：控制台只输出一行一行的普通日志，不依赖原地刷新，
// 在各种终端、远程桌面、复制粘贴和录屏里都一样稳；进度按阶段和步长打印，完整的实时进度在配置页和托盘提示里。
func (c *Console) SetLiveProgress(on bool) {
	c.mu.Lock()
	c.live = on
	c.mu.Unlock()
}

// Interactive 表示要用底部活动行（打开了 SetLiveProgress，且控制台是支持 VT 的终端）。
// 不用时调用方按步长打印普通行。
func (c *Console) Interactive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live && c.vt && !c.hidden && !c.closed
}

// Progress 设置或刷新底部活动行。frac 在 [0,1] 时画进度条，小于 0 时只显示文字（阶段进行中、没有进度可言）。
// 不能原地刷新时什么都不做。
func (c *Console) Progress(label string, frac float64, info string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.liveLabel, c.liveFrac, c.liveInfo, c.liveOn = label, frac, info, true
	if !c.vt || c.hidden || c.closed {
		return
	}
	line := renderLive(label, frac, info, c.cols)
	if c.liveShown {
		fmt.Fprint(c.out, "\r"+clear+line)
	} else {
		fmt.Fprint(c.out, "\r\n"+line)
	}
	c.liveShown = true
}

// ProgressDone 结束活动行：line 不为空时把它作为一条带时间的普通行留在原处（例如「✓ 下载完成 …」），
// 为空时擦掉活动行。不能原地刷新时 line 按普通行打印。
func (c *Console) ProgressDone(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	shown := c.liveShown
	c.liveOn, c.liveShown = false, false
	if c.closed || c.hidden {
		return
	}
	if !c.vt {
		if line != "" {
			fmt.Fprintf(c.out, "%s  %s\n", time.Now().Format("15:04:05"), line)
		}
		return
	}
	switch {
	case shown && line != "":
		fmt.Fprint(c.out, "\r"+clear+stamped(line))
	case shown:
		// 擦掉这一行，光标回到上一行：下一条输出以 \r\n 开头，正好落回这里。
		fmt.Fprint(c.out, "\r"+clear+"\x1b[1A")
	case line != "":
		fmt.Fprint(c.out, "\r\n"+stamped(line))
	}
}

// Line 写一条带时间的状态行（和日志行一样的时间前缀），只上屏、不进 inner.log。活动行在屏上时让开它。
func (c *Console) Line(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.hidden || text == "" {
		return
	}
	if !c.vt {
		fmt.Fprintf(c.out, "%s  %s\n", time.Now().Format("15:04:05"), text)
		return
	}
	c.lineLocked(stamped(text))
}

func stamped(text string) string {
	return faint + time.Now().Format("15:04:05") + reset + "  " + styleLine(text)
}

// lineLocked 写一条普通行（已上色）。活动行在屏上时，写在它的位置并把它重画到下一行。
func (c *Console) lineLocked(text string) {
	if !c.liveShown {
		fmt.Fprint(c.out, "\r\n"+text)
		return
	}
	fmt.Fprint(c.out, "\r"+clear+text+"\r\n"+renderLive(c.liveLabel, c.liveFrac, c.liveInfo, c.cols))
}

// redrawLiveLocked 在重新布局（窗口大小变化、换了控制台）后把活动行画回最后一行。
func (c *Console) redrawLiveLocked() {
	if !c.liveOn || !c.vt {
		c.liveShown = false
		return
	}
	fmt.Fprintf(c.out, "\x1b[%d;1H%s%s", c.rows, clear, renderLive(c.liveLabel, c.liveFrac, c.liveInfo, c.cols))
	c.liveShown = true
}

// renderLive 画一行：「 标签 ▕进度条▏ 42%  其余信息」，总宽度不超过 cols-1（不折行，否则 \r 回不到行首）。
func renderLive(label string, frac float64, info string, cols int) string {
	limit := cols - 1
	if limit < 1 {
		return ""
	}
	head := " " + label + " "
	if frac < 0 || math.IsNaN(frac) {
		return signal + clip(head, limit) + reset + cream + clip(info, max(0, limit-width(head))) + reset
	}
	frac = math.Min(1, math.Max(0, frac))
	pct := fmt.Sprintf(" %3d%% ", int(math.Floor(frac*100+1e-9)))
	if frac >= 1 {
		pct = " 100% "
	}
	fixed := width(head) + width(pct) + 2 // 进度条两侧各留一格
	cells := limit - fixed - min(width(info)+2, 34)
	cells = min(cells, barMax)
	if cells < barMin {
		// 太窄：不画条，只留文字。
		return signal + clip(head, limit) + reset + cream + clip(strings.TrimSpace(pct)+"  "+info, max(0, limit-width(head))) + reset
	}
	halves := int(math.Floor(frac * float64(cells*2)))
	full, half := halves/2, halves%2
	var b strings.Builder
	b.WriteString(signal + head + reset + " ")
	b.WriteString(barFill + strings.Repeat(" ", full))
	if half == 1 && full < cells {
		b.WriteString(barTrack + barHalf + "▌")
		full++
	}
	b.WriteString(barTrack + strings.Repeat(" ", cells-full) + reset + " ")
	b.WriteString(bold + cream + pct + reset)
	rest := limit - width(head) - 1 - cells - 1 - width(pct)
	if rest > 1 && info != "" {
		b.WriteString(" " + muted + clip(info, rest-1) + reset)
	}
	return b.String()
}
