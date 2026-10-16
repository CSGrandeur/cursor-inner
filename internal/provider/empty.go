package provider

import (
	"encoding/json"
	"log/slog"
	"strings"

	"cursor-inner/internal/config"
)

// emptyRawHead 是空回复时记录的上游原始响应前缀长度。
const emptyRawHead = 4096

// headBuffer 只保留写入内容的前 max 字节。
type headBuffer struct {
	max int
	buf []byte
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

func (h *headBuffer) Bytes() []byte { return h.buf }

// staticFinish 从非流式 JSON 回复里取 finish_reason / stop_reason。
func staticFinish(body []byte) string {
	var v struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		StopReason string `json:"stop_reason"`
		Status     string `json:"status"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if len(v.Choices) > 0 && v.Choices[0].FinishReason != "" {
		return v.Choices[0].FinishReason
	}
	if v.StopReason != "" {
		return v.StopReason
	}
	return v.Status
}

// emptyReply 记录空回复的诊断（finish_reason、用量、上游原始响应前 4KB）并构造错误。
// 只记上游返回的内容，不记请求体；万一响应里回显了密钥也会被抹掉。
// 请求带的图片多于保底张数时标记 Large，调用方可省略一批旧图片再试（参考 deepseek-harness 的 image offload）。
func emptyReply(m config.Model, messages []Message, reqBytes int, finish string, msg Message, raw []byte) error {
	images, imageBytes := CountImages(messages)
	large := images > ImageFloor
	slog.Warn("upstream_empty_detail 空回复诊断",
		"model", m.Model,
		"finish_reason", finish,
		"prompt_tokens", msg.PromptTokens,
		"completion_tokens", msg.CompletionTokens,
		"request_bytes", reqBytes,
		"images", images,
		"image_bytes", imageBytes,
		"raw_head", redactSecret(string(raw), m.APIKey),
	)
	err := emptyError().(*APIError)
	err.Large = large
	return err
}

func redactSecret(s, key string) string {
	if key = strings.TrimSpace(key); len(key) >= 4 {
		s = strings.ReplaceAll(s, key, "***")
	}
	return s
}
