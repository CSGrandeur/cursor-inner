package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/provider"
)

// 循环检测评测语料：区分「真的卡住了」与「重复但有产出」，避免误伤正常多轮。
type loopCase struct {
	name string
	seq  []provider.ToolCall
	loop bool // true=应当在某步停下；false=永远不该停
}

func call(name, path string) provider.ToolCall {
	return provider.ToolCall{Name: name, Arguments: `{"path":"` + path + `"}`}
}

func shell(cmd string) provider.ToolCall {
	return provider.ToolCall{Name: "Shell", Arguments: `{"command":"` + cmd + `"}`}
}

func loopCorpus() []loopCase {
	var osc []provider.ToolCall
	for i := 0; i < 8; i++ {
		osc = append(osc, call("Read", "/w/a.go"), call("Read", "/w/b.go"))
	}
	var inter []provider.ToolCall
	for i := 0; i < 4; i++ {
		inter = append(inter, call("Read", "/w/a.go"))
		if i < 3 {
			inter = append(inter, shell("go build"))
		}
	}
	var distinct []provider.ToolCall
	for _, f := range []string{"a", "b", "c", "d", "e", "f"} {
		distinct = append(distinct, call("Read", "/w/"+f+".go"))
	}
	same := func(n int) []provider.ToolCall {
		var out []provider.ToolCall
		for i := 0; i < n; i++ {
			out = append(out, call("Read", "/w/a.go"))
		}
		return out
	}
	return []loopCase{
		{"true-loop", same(6), true},
		{"interleaved-productive", inter, false},
		{"distinct-reads", distinct, false},
		{"ab-oscillation", osc, true},
		{"short-repeat", same(2), false},
	}
}

func runLoopCase(c loopCase) bool {
	tr := newLoopTracker()
	stopped := false
	for _, call := range c.seq {
		if _, stop := tr.observe([]provider.ToolCall{call}); stop {
			stopped = true
			break
		}
	}
	return stopped == c.loop
}

func TestEvalLoopDetection(t *testing.T) {
	corpus := loopCorpus()
	pass := 0
	for _, c := range corpus {
		if runLoopCase(c) {
			pass++
		} else {
			t.Logf("MISS %-24s loop=%v", c.name, c.loop)
		}
	}
	t.Logf("loop eval: %d/%d", pass, len(corpus))
	if pass < loopMinPass {
		t.Fatalf("loop eval regressed: %d/%d < %d", pass, len(corpus), loopMinPass)
	}
}

var loopMinPass = 5

// 压缩评测：验证超窗时缩短较早的工具输出、保留最近几条、不打乱工具调用配对。
func bigTool(id string, n int) provider.Message {
	return provider.Message{Role: "tool", ToolCallID: id, Content: strings.Repeat("x", n)}
}

func TestEvalCompaction(t *testing.T) {
	pass, total := 0, 0
	check := func(name string, ok bool) {
		total++
		if ok {
			pass++
		} else {
			t.Logf("MISS compaction %s", name)
		}
	}

	// 1) 没超线：原样返回。
	small := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok"},
	}
	check("under-threshold-untouched", estimateTokens(compactMessages(small, 128000)) == estimateTokens(small))

	// 2) 超线：较早的大工具输出被缩短，整体 token 下降。
	big := []provider.Message{{Role: "user", Content: "task"}}
	for i := 0; i < 10; i++ {
		big = append(big,
			provider.Message{Role: "assistant", Content: "step"},
			bigTool("t"+strings.Repeat("x", i+1), 8000))
	}
	before := estimateTokens(big)
	after := compactMessages(big, 20000)
	check("over-threshold-shrinks", estimateTokens(after) < before)
	// 最近 6 条保持原样（没有被压缩标记污染）。
	tail := after[len(after)-1]
	check("recent-tail-intact", !strings.Contains(tail.Content, "compacted"))

	// 3) forceCompact：强压后 token 严格下降，且不以孤立 tool 消息开头。
	forced := forceCompact(big)
	check("force-shrinks", estimateTokens(forced) < before)
	check("no-orphan-tool-head", forced[0].Role != "tool")

	t.Logf("compaction eval: %d/%d", pass, total)
	if pass < total {
		t.Fatalf("compaction eval regressed: %d/%d", pass, total)
	}
}

// 正文复读评测：复读应判定为真，正常排版（含表格分隔线）不应误判。
func TestEvalContentLoop(t *testing.T) {
	table := strings.Repeat("| --- | --- | --- | --- | --- | --- |\n", 20)
	varied := "The function returned the wrong value because the branch condition " +
		"was inverted; after swapping it the test suite passes and the output matches."
	cases := []struct {
		name  string
		text  string
		chant bool
	}{
		{"repeated-sentence", strings.Repeat("I will now fix the bug in the file. ", 40), true},
		{"repeated-word", strings.Repeat("error ", 300), true},
		{"char-run", strings.Repeat("x", 2000), true},
		{"markdown-table", table, false},
		{"varied-prose", varied, false},
		{"short", "hello world", false},
	}
	pass := 0
	for _, c := range cases {
		if chantDetected(c.text) == c.chant {
			pass++
		} else {
			t.Logf("MISS content %-18s want=%v", c.name, c.chant)
		}
	}
	t.Logf("content-loop eval: %d/%d", pass, len(cases))
	if pass < len(cases) {
		t.Fatalf("content-loop eval regressed: %d/%d", pass, len(cases))
	}
}
