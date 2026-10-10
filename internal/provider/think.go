package provider

import "strings"

// inlineThink 把流式内容里的 <think>…</think> / <thinking>…</thinking> 推理标签
// 从正文里剥出来：标签内文本走思考通道，其余走正文。跨分片的半截标签先缓存，补齐后再判定。
// 不少 vLLM/Ollama 上的 Qwen3、QwQ 直接把推理写进 content（而非 reasoning_content），
// 不剥会把思考混进答案，也会干扰文本工具调用的救援解析。参考 qwen-code 的 taggedThinkingParser。
type inlineThink struct {
	inThought bool
	buf       string
}

var thinkOpenTags = []string{"<think>", "<thinking>"}
var thinkCloseTags = []string{"</think>", "</thinking>"}

const maxThinkTag = 11 // len("</thinking>")

func thinkTagAt(low string, off int, tags []string) string {
	for _, t := range tags {
		if off+len(t) <= len(low) && low[off:off+len(t)] == t {
			return t
		}
	}
	return ""
}

func thinkPartialAt(low string, off int, tags []string) bool {
	rem := low[off:]
	if len(rem) >= maxThinkTag {
		return false
	}
	for _, t := range tags {
		if strings.HasPrefix(t, rem) {
			return true
		}
	}
	return false
}

// feed 处理一段内容增量，返回应分别输出到正文与思考通道的文本。
func (p *inlineThink) feed(s string) (text, thought string) {
	if s == "" {
		return "", ""
	}
	p.buf += s
	data := p.buf
	low := strings.ToLower(data)
	var tb, hb strings.Builder
	emit := func(str string) {
		if p.inThought {
			hb.WriteString(str)
		} else {
			tb.WriteString(str)
		}
	}
	i := 0
	for i < len(data) {
		if data[i] == '<' {
			tags := thinkOpenTags
			if p.inThought {
				tags = thinkCloseTags
			}
			if tag := thinkTagAt(low, i, tags); tag != "" {
				p.inThought = !p.inThought
				i += len(tag)
				continue
			}
			if thinkPartialAt(low, i, tags) {
				break // 半截标签，留到下一片补齐
			}
		}
		emit(data[i : i+1])
		i++
	}
	p.buf = data[i:]
	return tb.String(), hb.String()
}

// flush 在流结束时把残留（未闭合的半截标签）按当前通道原样吐出。
func (p *inlineThink) flush() (text, thought string) {
	if p.buf == "" {
		return "", ""
	}
	s := p.buf
	p.buf = ""
	if p.inThought {
		return "", s
	}
	return s, ""
}
