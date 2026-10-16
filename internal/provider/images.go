package provider

import (
	"fmt"

	"cursor-inner/internal/config"
)

// 请求里图片的保留策略，照 hermes-agent agent/image_eviction_policy.py：
// 不按“只留最新 N 张”滚动省略（那样每来一张新图都会改动已缓存的前缀），
// 而是在超过张数或字节上限时，从最旧的工具图片起按批省略，省略张数随超出量阶梯式增长，
// 两次越线之间请求前缀保持不变；最新的几张始终保留。
const (
	DefaultMaxImages   = 20
	DefaultImageBudget = 24_000_000 // 字节，按 base64 后的大小计
	ImageEvictBatch    = 8
	ImageFloor         = 3
)

// ImageLimits 返回模型的图片张数上限和字节预算（0 表示用默认值）。
func ImageLimits(m config.Model) (int, int) {
	limit, budget := DefaultMaxImages, DefaultImageBudget
	if m.MaxImages > 0 {
		limit = m.MaxImages
	}
	if m.ImageBudgetMB > 0 {
		budget = m.ImageBudgetMB * 1_000_000
	}
	return limit, budget
}

func encodedSize(n int) int { return (n + 2) / 3 * 4 }

// CountImages 统计消息里的图片张数和 base64 后的总字节数。
func CountImages(messages []Message) (int, int) {
	count, size := 0, 0
	for _, m := range messages {
		for _, img := range m.Images {
			count++
			size += encodedSize(len(img.Data))
		}
	}
	return count, size
}

// carriers 返回带图片的工具消息下标（从旧到新）。用户自己贴的图不省略，只计入预算。
func carriers(messages []Message) []int {
	var out []int
	for i, m := range messages {
		if m.Role == "tool" && len(m.Images) > 0 {
			out = append(out, i)
		}
	}
	return out
}

// retireCount 计算要省略多少条最旧的带图工具消息。
func retireCount(messages []Message, limit, budget int) int {
	idx := carriers(messages)
	total := len(idx)
	if total == 0 {
		return 0
	}
	reservedN, reservedB := 0, 0
	for _, m := range messages {
		if m.Role == "tool" {
			continue
		}
		for _, img := range m.Images {
			reservedN++
			reservedB += encodedSize(len(img.Data))
		}
	}
	// window：从最新往回数，能放进上限的带图消息条数。
	window, n, b := 0, reservedN, reservedB
	for k := total - 1; k >= 0; k-- {
		m := messages[idx[k]]
		mb := 0
		for _, img := range m.Images {
			mb += encodedSize(len(img.Data))
		}
		if n+len(m.Images) > limit || b+mb > budget {
			break
		}
		n += len(m.Images)
		b += mb
		window++
	}
	if window >= total {
		return 0
	}
	need := total - window
	quantum := ImageEvictBatch
	if q := window - ImageFloor; q < quantum {
		quantum = max(q, 1)
	}
	retire := (need + quantum - 1) / quantum * quantum
	return min(retire, max(total-ImageFloor, 0))
}

// EvictImages 在超过张数或字节上限时省略最旧的一批工具图片；没超就原样返回。
func EvictImages(messages []Message, m config.Model) []Message {
	limit, budget := ImageLimits(m)
	return retireOldest(messages, retireCount(messages, limit, budget))
}

// EvictImageBatch 再省略一批最旧的工具图片（保留最新 ImageFloor 条）。返回是否有省略。
func EvictImageBatch(messages []Message) ([]Message, bool) {
	n := min(ImageEvictBatch, max(len(carriers(messages))-ImageFloor, 0))
	if n == 0 {
		return messages, false
	}
	return retireOldest(messages, n), true
}

// ImageOmitted 是省略图片后留给模型的占位文字前缀。
const ImageOmitted = "[之前的图片已省略"

func retireOldest(messages []Message, n int) []Message {
	if n <= 0 {
		return messages
	}
	out := append([]Message(nil), messages...)
	for _, i := range carriers(out) {
		if n == 0 {
			break
		}
		size := 0
		for _, img := range out[i].Images {
			size += len(img.Data)
		}
		out[i].Images = nil
		out[i].Content += fmt.Sprintf("\n%s，%d 字节]", ImageOmitted, size)
		n--
	}
	return out
}
