package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"cursor-inner/internal/provider"
)

const (
	loopWarnAt     = 3 // 连续相同调用到第 3 次：结果侧 reminder（对齐 qwen loopProtection 默认 warn）
	loopHardStopAt = 8 // 仅连续相同指纹到第 8 次才暂停；夹了其它工具的轮询不再靠累计硬停

	// oscillationCycles：完整 A-B 周期数。3 周期 = 6 次调用（A B A B A B）硬停。
	// 去掉累计 counts 硬停后，用这条兜住真交替死循环。
	oscillationCycles = 3

	computerStreakWarnAt = 3 // 连续 computer_* 到第 3 次提醒改用 Shell/读写

	contentChunk    = 50   // 正文复读检测的窗口大小（字符）
	contentLoopAt   = 12   // 同一 50 字窗口重复到这个次数判定为复读
	contentWindow   = 4000 // 只在最近这么多字里找复读
	contentMinAlnum = 10   // 窗口里字母数字太少（表格线、分隔符）不计，降低误判
)

// loopHaltKind 区分硬停原因，供暂停文案使用。
type loopHaltKind int

const (
	loopHaltNone loopHaltKind = iota
	loopHaltIdentical
	loopHaltOscillation
)

// loopTracker 识别工具调用复读、连续 Computer Use，以及正文复读。
// 默认行为对齐 qwen-code model.loopProtection=warn：到提醒点时把简短 reminder
// 挂进工具结果，不暂停本轮；硬停只认「连续相同指纹」与「A-B 振荡」，
// 不再用整轮累计同一指纹触发硬停（合法 Read+sleep 轮询会误伤）。
// 正文复读在流式回调里累积文本，命中后让本次生成提前收尾。
type loopTracker struct {
	consec int
	lastFP string

	// recentFP / recentName：最近工具指纹与名称，用于 A-B 振荡检测。
	recentFP   []string
	recentName []string

	computerStreak   int
	remindedIdent    bool
	remindedComputer bool

	pending   string
	pendingID string

	halt      loopHaltKind
	haltName  string
	haltNameB string

	text     strings.Builder
	lastScan int
}

func newLoopTracker() *loopTracker {
	return &loopTracker{}
}

func toolFingerprint(call provider.ToolCall) string {
	sum := sha256.Sum256([]byte(strings.ToLower(call.Name) + "\n" + strings.TrimSpace(call.Arguments)))
	return hex.EncodeToString(sum[:8])
}

// isComputerTool 识别 Cursor Computer Use MCP 工具名（computer_screenshot 等）。
func isComputerTool(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	if strings.HasPrefix(n, "computer_") {
		return true
	}
	return strings.Contains(n, "computer_use") || strings.Contains(n, "_computer_")
}

func identicalReminder(name string, count int) string {
	return fmt.Sprintf("System: `%s` has been called %d times in a row with identical arguments, so its result will not change. Use the result you already have, or change the approach.", name, count)
}

func computerStreakReminder() string {
	return "System: Several Computer Use actions ran in a row. For terminal commands and file edits, prefer Shell, Read, Write, or StrReplace when those tools are available; keep Computer Use for GUI work."
}

func (t *loopTracker) queueReminder(callID, text string) {
	if text == "" {
		return
	}
	if t.pending == "" {
		t.pending = text
		t.pendingID = callID
		return
	}
	if strings.Contains(t.pending, text) {
		return
	}
	t.pending = t.pending + "\n" + text
	if t.pendingID == "" {
		t.pendingID = callID
	}
}

// takeReminder 取出并清空待挂到工具结果上的 reminder（不校验 call id）。
func (t *loopTracker) takeReminder() string {
	s := t.pending
	t.pending = ""
	t.pendingID = ""
	return s
}

// takeReminderFor 仅当 pending 针对该 call（或尚未绑定 id）时取出。
func (t *loopTracker) takeReminderFor(callID string) string {
	if t.pending == "" {
		return ""
	}
	if t.pendingID != "" && callID != "" && t.pendingID != callID {
		return ""
	}
	return t.takeReminder()
}

// appendLoopReminder 把 loop 提醒附在工具结果末尾，供模型在下一轮看到。
func appendLoopReminder(text, reminder string) string {
	if reminder == "" {
		return text
	}
	if text == "" {
		return reminder
	}
	return text + "\n\n" + reminder
}

func (t *loopTracker) pushRecent(fp, name string) {
	t.recentFP = append(t.recentFP, fp)
	t.recentName = append(t.recentName, name)
	// 只保留振荡检测所需窗口的两倍，避免无界增长。
	max := oscillationCycles * 4
	if len(t.recentFP) > max {
		t.recentFP = append([]string(nil), t.recentFP[len(t.recentFP)-max:]...)
		t.recentName = append([]string(nil), t.recentName[len(t.recentName)-max:]...)
	}
}

// oscillating 检测最近 2*cycles 次调用是否严格交替为 A B A B …（指纹 A≠B）。
// 只在「同一工具名、不同参数」时硬停（典型：Read a / Read b 空转）。
// Read + Shell/sleep 轮询工具名不同，不算振荡，避免误伤合法长等待。
func (t *loopTracker) oscillating() (ok bool, nameA, nameB string) {
	n := oscillationCycles * 2
	if len(t.recentFP) < n {
		return false, "", ""
	}
	hist := t.recentFP[len(t.recentFP)-n:]
	names := t.recentName[len(t.recentName)-n:]
	a, b := hist[0], hist[1]
	if a == "" || b == "" || a == b {
		return false, "", ""
	}
	tool := names[0]
	if tool == "" {
		return false, "", ""
	}
	for i := 0; i < n; i++ {
		want := a
		if i%2 == 1 {
			want = b
		}
		if hist[i] != want || names[i] != tool {
			return false, "", ""
		}
	}
	return true, names[0], names[1]
}

// observe 在执行一批工具前调用。stop 为真时应中止本轮。
// 提醒通过 takeReminder / takeReminderFor 取出，挂进对应工具结果，不再伪造 user 消息。
func (t *loopTracker) observe(calls []provider.ToolCall) (stop bool) {
	for _, call := range calls {
		fp := toolFingerprint(call)
		if fp == t.lastFP {
			t.consec++
		} else {
			t.consec = 1
			t.lastFP = fp
			t.remindedIdent = false
		}
		t.pushRecent(fp, call.Name)

		if isComputerTool(call.Name) {
			t.computerStreak++
		} else {
			t.computerStreak = 0
			t.remindedComputer = false
		}

		if t.consec >= loopHardStopAt {
			t.halt = loopHaltIdentical
			t.haltName = call.Name
			t.haltNameB = ""
			return true
		}
		if ok, a, b := t.oscillating(); ok {
			t.halt = loopHaltOscillation
			t.haltName = a
			t.haltNameB = b
			return true
		}
		if t.consec == loopWarnAt && !t.remindedIdent {
			t.remindedIdent = true
			t.queueReminder(call.ID, identicalReminder(call.Name, t.consec))
		}
		if t.computerStreak == computerStreakWarnAt && !t.remindedComputer {
			t.remindedComputer = true
			t.queueReminder(call.ID, computerStreakReminder())
		}
	}
	return false
}

// haltKind 返回最近一次 observe 触发的硬停类型。
func (t *loopTracker) haltKind() loopHaltKind { return t.halt }

// haltNames 返回硬停相关工具名（振荡时为 A/B，否则 B 为空）。
func (t *loopTracker) haltNames() (a, b string) { return t.haltName, t.haltNameB }

// resetText 在每次向模型请求前清空正文累积。
func (t *loopTracker) resetText() {
	t.text.Reset()
	t.lastScan = 0
}

// feedText 累积一段流式正文，返回是否判定为复读。为省开销，只在新增够一个窗口时才扫描。
func (t *loopTracker) feedText(delta string) bool {
	if delta == "" {
		return false
	}
	t.text.WriteString(delta)
	if t.text.Len()-t.lastScan < contentChunk {
		return false
	}
	t.lastScan = t.text.Len()
	return chantDetected(t.text.String())
}

// chantDetected 在最近 contentWindow 字里找是否有某个 50 字窗口重复到阈值。
// 跳过字母数字过少的窗口（表格线、Markdown 分隔），避免把正常排版当成复读。
func chantDetected(text string) bool {
	if len(text) < contentChunk*2 {
		return false
	}
	if len(text) > contentWindow {
		text = text[len(text)-contentWindow:]
	}
	seen := make(map[string]int, len(text))
	for i := 0; i+contentChunk <= len(text); i++ {
		w := text[i : i+contentChunk]
		if alnumCount(w) < contentMinAlnum {
			continue
		}
		seen[w]++
		if seen[w] >= contentLoopAt {
			return true
		}
	}
	return false
}

func alnumCount(s string) int {
	n := 0
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			n++
		}
	}
	return n
}
