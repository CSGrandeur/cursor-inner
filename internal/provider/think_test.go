package provider

import "testing"

// 把一串分片喂进 inlineThink，拼出正文与思考两路结果（含结束 flush）。
func runThink(chunks []string) (text, thought string) {
	p := &inlineThink{}
	for _, c := range chunks {
		tx, th := p.feed(c)
		text += tx
		thought += th
	}
	tx, th := p.flush()
	return text + tx, thought + th
}

func TestInlineThinkWholeTag(t *testing.T) {
	tx, th := runThink([]string{"<think>reasoning here</think>answer"})
	if tx != "answer" {
		t.Fatalf("text = %q, want %q", tx, "answer")
	}
	if th != "reasoning here" {
		t.Fatalf("thought = %q, want %q", th, "reasoning here")
	}
}

func TestInlineThinkSplitAcrossChunks(t *testing.T) {
	// 标签被切成任意碎片，仍要正确归类
	tx, th := runThink([]string{"<thi", "nk>he", "llo</thin", "k>wor", "ld"})
	if tx != "world" {
		t.Fatalf("text = %q, want %q", tx, "world")
	}
	if th != "hello" {
		t.Fatalf("thought = %q, want %q", th, "hello")
	}
}

func TestInlineThinkThinkingVariant(t *testing.T) {
	tx, th := runThink([]string{"pre<thinking>mid</thinking>post"})
	if tx != "prepost" || th != "mid" {
		t.Fatalf("text=%q thought=%q", tx, th)
	}
}

func TestInlineThinkNoTagsPassthrough(t *testing.T) {
	// 没有思考标签时正文原样通过，是无害的 no-op
	tx, th := runThink([]string{"just ", "plain ", "text"})
	if tx != "just plain text" || th != "" {
		t.Fatalf("text=%q thought=%q", tx, th)
	}
}

func TestInlineThinkLiteralAngleBracket(t *testing.T) {
	// 非思考标签的 '<' 不能被吞掉
	tx, th := runThink([]string{"a < b and <div>x</div>"})
	if tx != "a < b and <div>x</div>" || th != "" {
		t.Fatalf("text=%q thought=%q", tx, th)
	}
}

func TestInlineThinkUnterminatedFlushedAsThought(t *testing.T) {
	// 流在思考中断掉：残留按思考通道吐出，不丢字符
	tx, th := runThink([]string{"<think>still thinking"})
	if tx != "" || th != "still thinking" {
		t.Fatalf("text=%q thought=%q", tx, th)
	}
}

func TestInlineThinkTrailingPartialTagIsText(t *testing.T) {
	// 结尾残留一个像半截标签的 '<th'，没补齐就 flush，应原样当正文
	tx, th := runThink([]string{"done<th"})
	if tx != "done<th" || th != "" {
		t.Fatalf("text=%q thought=%q", tx, th)
	}
}

func TestInlineThinkMultibyte(t *testing.T) {
	tx, th := runThink([]string{"答案<think>思考中</think>完成"})
	if tx != "答案完成" || th != "思考中" {
		t.Fatalf("text=%q thought=%q", tx, th)
	}
}
