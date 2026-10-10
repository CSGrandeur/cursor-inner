package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"cursor-inner/internal/provider"
)

const (
	loopWarnAt     = 3 // 连续相同调用到第 3 次给出警告
	loopStopAt     = 5 // 连续相同调用到第 5 次停止；压在 DashScope 服务端「重复调用」判定之下，避免整段对话被 400（见 qwen-code #5019）
	loopHardStopAt = 8 // 同一调用累计到第 8 次停止，兜底 A/B 交替这类非连续死循环

	contentChunk    = 50   // 正文复读检测的窗口大小（字符）
	contentLoopAt   = 12   // 同一 50 字窗口重复到这个次数判定为复读
	contentWindow   = 4000 // 只在最近这么多字里找复读
	contentMinAlnum = 10   // 窗口里字母数字太少（表格线、分隔符）不计，降低误判
)

// loopTracker 识别两类卡死：工具调用复读与正文复读。
// 工具调用用指纹计数：连续相同到 loopStopAt 停，或同一指纹累计到 loopHardStopAt 停（兜底交替循环）。
// 正文复读在流式回调里累积文本，命中后让本次生成提前收尾。借鉴 qwen-code 的 loopDetectionService。
type loopTracker struct {
	counts map[string]int
	consec int
	lastFP string
	warn   string

	text     strings.Builder
	lastScan int
}

func newLoopTracker() *loopTracker {
	return &loopTracker{counts: map[string]int{}}
}

func toolFingerprint(call provider.ToolCall) string {
	sum := sha256.Sum256([]byte(strings.ToLower(call.Name) + "\n" + strings.TrimSpace(call.Arguments)))
	return hex.EncodeToString(sum[:8])
}

// observe 在执行一批工具前调用。stop 为真时应中止本轮；warn 非空时应写入提示。
func (t *loopTracker) observe(calls []provider.ToolCall) (warn string, stop bool) {
	for _, call := range calls {
		fp := toolFingerprint(call)
		t.counts[fp]++
		if fp == t.lastFP {
			t.consec++
		} else {
			t.consec = 1
			t.lastFP = fp
		}
		if t.consec >= loopStopAt || t.counts[fp] >= loopHardStopAt {
			return "", true
		}
		if t.consec == loopWarnAt {
			t.warn = fmt.Sprintf("Warning: tool %s with the same arguments was called %d times in a row. Do not repeat it; change approach or stop.", call.Name, t.consec)
			return t.warn, false
		}
	}
	return "", false
}

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
