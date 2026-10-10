package provider

import "testing"

func TestAnthropicStreamUsageCountsCache(t *testing.T) {
	a := newAccumulator("anthropic", false)
	for _, line := range []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":3000,"cache_creation_input_tokens":200,"output_tokens":1}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
	} {
		if _, err := a.feed([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	msg := a.message()
	if msg.PromptTokens != 3212 || msg.CacheTokens != 3000 || msg.CompletionTokens != 42 {
		t.Fatalf("prompt %d cache %d completion %d", msg.PromptTokens, msg.CacheTokens, msg.CompletionTokens)
	}
}
